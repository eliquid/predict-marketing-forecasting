package main

// Storage. Three tables, one SQLite file, no ORM.
//
//   series     the numbers you fed in
//   runs       one row per forecast, with everything needed to say what produced it
//   forecasts  the answer: one row per day per quantile
//
// Forecasts are stored long rather than as nine q10..q90 columns. Adding a model
// with a different set of quantiles then needs no schema change -- Chronos-2
// returns 21 of them, TimesFM 3.0 returns 9.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
-- Every input row exactly as it arrived, whatever shape the file was. This is the
-- history to go back to for ad-hoc questions: it keeps the text columns, the
-- per-campaign detail and the original spelling of every value, none of which
-- survive into the aggregated series.
--
-- data is a JSON object of column -> value as written. Query it with SQLite's
-- json_extract, e.g.
--   SELECT day, json_extract(data,'$.Campaign'), json_extract(data,'$.Cost')
--   FROM raw WHERE source='acme' AND json_extract(data,'$."Campaign status"')='Enabled';
CREATE TABLE IF NOT EXISTS raw (
    source  TEXT NOT NULL,
    row_num INTEGER NOT NULL,        -- line number in the source file
    day     TEXT NOT NULL,
    data    TEXT NOT NULL,
    PRIMARY KEY (source, row_num)
);


-- Forecast against what actually happened.
--
-- A view rather than a table: every number in it already exists in forecasts and
-- series, so copying them into a third place would only let the copies drift.
-- The actual is NULL until the day arrives and a newer export is imported, which
-- is what makes this worth keeping -- run it again next week and the same rows
-- fill in.
--
--   SELECT model, metric, days_ahead,
--          ROUND(AVG(ABS(pct_error)),1) AS mape
--   FROM forecast_accuracy
--   WHERE entity='(account)' AND actual IS NOT NULL AND trained_on = 0
--   GROUP BY model, metric, days_ahead;
--
-- Always filter trained_on = 0. A fine-tuned model has seen the days it was
-- trained on, and scoring against them flatters it enormously.

-- One row per number per day. "metric" is the target's column name, or a
-- covariate's, so a forecast that used a covariate has that covariate stored
-- too and the run can be explained later.
-- entity is a campaign name, or "(account)" for the total across all of them.
CREATE TABLE IF NOT EXISTS series (
    series_id TEXT NOT NULL,
    entity    TEXT NOT NULL,
    metric    TEXT NOT NULL,
    day       TEXT NOT NULL,          -- YYYY-MM-DD
    value     REAL NOT NULL,
    PRIMARY KEY (series_id, entity, metric, day)
);

CREATE TABLE IF NOT EXISTS runs (
    id           TEXT PRIMARY KEY,
    series_id    TEXT NOT NULL,
    model        TEXT NOT NULL,
    horizon      INTEGER NOT NULL,
    created_at   TEXT NOT NULL,       -- when the forecast was produced
    as_of        TEXT NOT NULL,       -- last day of real data it was based on
    metrics      TEXT NOT NULL,       -- the metrics forecast, comma separated
    entities     TEXT NOT NULL,       -- what was forecast: "(account)" and each campaign
    group_by     TEXT NOT NULL,       -- the column campaigns were split on, if any
    input_sha256 TEXT NOT NULL,       -- hash of the exact numbers sent to the model
    model_info   TEXT NOT NULL        -- the worker's handshake, stored verbatim
);

-- Both models are multivariate, so one run forecasts several metrics together
-- and each forecast row says which metric it belongs to.
CREATE TABLE IF NOT EXISTS forecasts (
    run_id   TEXT NOT NULL,
    entity   TEXT NOT NULL,
    metric   TEXT NOT NULL,
    day      TEXT NOT NULL,
    quantile REAL NOT NULL,
    value    REAL NOT NULL,
    PRIMARY KEY (run_id, entity, metric, day, quantile),
    FOREIGN KEY (run_id) REFERENCES runs(id)
);

-- quantile is the last column of the primary key, so a query that constrains
-- only quantile -- which is every query through forecast_accuracy, because the
-- view ends "WHERE f.quantile = 0.5" -- cannot search that index and scans the
-- whole table instead. Measured on a 2.3M-row file: the accuracy query took
-- 1.6s scanning, 12ms searching this index, and the filter-validation query in
-- cmdAccuracy went from 1.16s to 1ms. Partial, so it holds one row per
-- (run, entity, metric, day) rather than one per quantile.
`

// derivedObjects are the view and the indexes: everything in the file that holds
// no data of its own and can be rebuilt from the tables at any time.
//
// They are kept apart from `schema` because `CREATE ... IF NOT EXISTS` is not a
// migration for them either, and unlike a table there is nothing for staleTable
// to inspect -- a view has no columns of its own to miss. A file carrying an old
// definition was therefore adopted in silence and kept it: a stale
// forecast_accuracy made `accuracy` exit 0 reporting "no forecast day has an
// actual yet" on a database holding 350 scorable rows.
//
// So these are compared against the file on every open and rebuilt when they
// differ. Rebuilding costs nothing -- the view is a query, the indexes are
// derivable -- and the comparison is a pure read, which is what keeps opening an
// unchanged database free of writes and still possible on read-only media.
var derivedObjects = []struct{ name, ddl string }{
	{"raw_day", `CREATE INDEX raw_day ON raw (source, day)`},

	// Forecast against what actually happened.
	//
	// A view rather than a table: every number in it already exists in forecasts
	// and series, so materialising it would be a second copy that can disagree
	// with the first.
	{"forecast_accuracy", `CREATE VIEW forecast_accuracy AS
SELECT
    r.series_id,
    r.model,
    r.as_of,
    CAST(julianday(f.day) - julianday(r.as_of) AS INTEGER) AS days_ahead,
    f.entity,
    f.metric,
    f.day,
    f.value                                   AS forecast,
    s.value                                   AS actual,
    f.value - s.value                          AS error,
    ABS(f.value - s.value)                     AS abs_error,
    CASE WHEN s.value <> 0
         THEN 100.0 * (f.value - s.value) / s.value END AS pct_error,
    lo.value                                   AS low,
    hi.value                                   AS high,
    CASE WHEN s.value IS NOT NULL AND lo.value IS NOT NULL
         THEN s.value BETWEEN lo.value AND hi.value END AS inside_range,
    -- A fine-tuned model has already seen the days it was trained on. Scoring it
    -- against them measures memorisation, not forecasting, and it would look
    -- spectacular for the wrong reason. The worker declares trained_through in
    -- its handshake, which is stored verbatim in model_info.
    CASE WHEN json_extract(r.model_info, '$.trained_through') IS NOT NULL
          AND f.day <= json_extract(r.model_info, '$.trained_through')
         THEN 1 ELSE 0 END AS trained_on,
    json_extract(r.model_info, '$.trained_through') AS trained_through
FROM forecasts f
JOIN runs    r  ON r.id = f.run_id
LEFT JOIN series s  ON s.series_id = r.series_id AND s.entity = f.entity
                   AND s.metric   = f.metric     AND s.day    = f.day
LEFT JOIN forecasts lo ON lo.run_id = f.run_id AND lo.entity = f.entity
                      AND lo.metric = f.metric AND lo.day = f.day AND lo.quantile = 0.1
LEFT JOIN forecasts hi ON hi.run_id = f.run_id AND hi.entity = f.entity
                      AND hi.metric = f.metric AND hi.day = f.day AND hi.quantile = 0.9
WHERE f.quantile = 0.5`},

	// quantile is the last column of the forecasts primary key, so a query that
	// constrains only quantile -- which is every query through forecast_accuracy,
	// because the view ends "WHERE f.quantile = 0.5" -- cannot search that index
	// and scans the whole table instead. Measured on a 2.3M-row file: the
	// accuracy query took 1.6s scanning, 12ms searching this index, and the
	// filter-validation query in cmdAccuracy went from 1.16s to 1ms. Partial, so
	// it holds one row per (run, entity, metric, day) rather than one per
	// quantile.
	{"forecasts_median", `CREATE INDEX forecasts_median
    ON forecasts (entity, run_id, metric, day) WHERE quantile = 0.5`},
}

// refreshDerived rebuilds any view or index whose definition in the file is not
// the one this binary expects. SQLite stores the CREATE statement verbatim, so
// comparing text is exact.
func refreshDerived(db *sql.DB) error {
	for _, o := range derivedObjects {
		var have, kind string
		err := db.QueryRow(`SELECT type, sql FROM sqlite_master WHERE name = ?`,
			o.name).Scan(&kind, &have)
		switch {
		case err == sql.ErrNoRows:
			kind = "" // absent: create it below
		case err != nil:
			return fmt.Errorf("reading the definition of %s: %w", o.name, err)
		case strings.TrimSpace(have) == strings.TrimSpace(o.ddl):
			continue // already what we want, and nothing is written
		}
		drop := "DROP INDEX IF EXISTS "
		if strings.HasPrefix(o.ddl, "CREATE VIEW") {
			drop = "DROP VIEW IF EXISTS "
		}
		if kind != "" {
			if _, err := db.Exec(drop + o.name); err != nil {
				return fmt.Errorf("replacing %s: %w", o.name, err)
			}
		}
		// Another process opening the same file at the same moment may have got
		// there first. Whoever won created the definition this binary wants --
		// they are running the same code -- so losing the race is success, the
		// same way it is for journal_mode above.
		if _, err := db.Exec(o.ddl); err != nil && !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("creating %s: %w", o.name, err)
		}
	}
	return nil
}

// schemaVersion is stamped into the file with PRAGMA user_version. Bump it when
// a table changes shape, and teach staleTable how to recognise the new one.
const schemaVersion = 1

// usablePath rejects the -db values that SQLite accepts but does not mean.
//
// An empty path opens an anonymous temporary database that is deleted on close,
// so a forecast reported "saved run ... to " and stored nothing. A path whose
// directory is missing, or which is itself a directory, comes back from the
// driver as "unable to open database file: out of memory", which is the least
// helpful thing to say to someone who mistyped a path.
func usablePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("no database file given: -db needs a filename, and an " +
			"empty one would be discarded when the program exits")
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return fmt.Errorf("-db %s is a directory, not a database file", path)
	}
	dir := filepath.Dir(path)
	if fi, err := os.Stat(dir); err != nil {
		return fmt.Errorf("-db %s: the directory %s does not exist", path, dir)
	} else if !fi.IsDir() {
		return fmt.Errorf("-db %s: %s is not a directory", path, dir)
	}
	return nil
}

func openDB(path string) (*sql.DB, error) {
	if err := usablePath(path); err != nil {
		return nil, err
	}
	// Both pragmas are per-connection, so they belong in the DSN, where the driver
	// applies them to every connection it opens rather than just the first.
	//
	// busy_timeout makes a blocked writer wait instead of failing instantly; without
	// it two forecasts started together collide and one dies with SQLITE_BUSY.
	//
	// foreign_keys is off by default in SQLite, which made the run_id reference in
	// forecasts decorative -- it was declared and never enforced, so a forecast row
	// could outlive the run that explains it.
	//
	// synchronous is deliberately left at the driver's default of FULL. This writes
	// once per forecast and is not latency-bound, so there is nothing to buy by
	// weakening durability.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)",
		url.PathEscape(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	// One connection per handle. SQLite serialises writes anyway, and a pool only
	// adds contention between this process's own connections.
	db.SetMaxOpenConns(1)

	// WAL lets a reader and a writer work at the same time. It is a persistent
	// property of the file, so it only has to be set once ever -- switching it
	// needs a brief exclusive lock, and a concurrent opener that loses that race
	// is fine, because whoever won has already left the database in WAL mode.
	_, _ = db.Exec("PRAGMA journal_mode=WAL")

	// The check comes first. Creating the schema over a file from before per-entity
	// storage fails on the first statement that names a column that file lacks,
	// which is exactly the unexplained error this is here to replace.
	current, err := checkSchema(db, path)
	if err != nil {
		db.Close()
		return nil, err
	}
	// A file already stamped at this version was created by this schema, so there
	// is nothing to add. Skipping both statements keeps opening it a pure read,
	// which is what lets a database on read-only media still be queried.
	if !current {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("opening database %s: %w", path, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			db.Close()
			return nil, fmt.Errorf("stamping schema version on %s: %w", path, err)
		}
	}

	// The view and the indexes are checked on every open, current file or not:
	// they are the part `schemaVersion` cannot speak for, because a stale one is
	// invisible to staleTable and answers queries wrongly rather than failing.
	// Unchanged, this writes nothing.
	if err := refreshDerived(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}

	// The file holds the account's whole spend history, and the default umask
	// leaves it readable by everyone on the machine. A failure here is not fatal:
	// the owner can still read it, and refusing to forecast over a filesystem that
	// cannot chmod would be a worse outcome than permissions wider than intended.
	// The sidecars only exist while the database is open.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(p, 0o600)
	}
	return db, nil
}

// checkSchema refuses a file this build would misread.
//
// CREATE TABLE IF NOT EXISTS is not a migration: on a file that already has the
// table it ignores the new definition entirely, without error. A database from
// before per-entity storage therefore keeps its old columns, every statement
// above appears to succeed, and the first real query fails with a bare
// "no such column: entity" that says nothing about the actual problem.
// It reports whether the file is already stamped at this version, and so needs
// nothing written to it.
func checkSchema(db *sql.DB, path string) (current bool, err error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return false, fmt.Errorf("reading schema version of %s: %w", path, err)
	}
	if v == schemaVersion {
		return true, nil
	}
	if v > schemaVersion {
		return false, fmt.Errorf("database %s was written by a newer version of this "+
			"tool (its schema is %d, this build understands %d)", path, v, schemaVersion)
	}

	// Version 0 is one of three things: a new or empty file, a file made before
	// stamping began that is still the current shape, or one genuinely older. Only
	// the last has to be refused, and the shape decides that, not the number.
	stale, err := staleTable(db)
	if err != nil {
		return false, err
	}
	if stale != "" {
		return false, fmt.Errorf("database %s predates per-entity storage and cannot "+
			"be read by this build: its %s table has no %s column, and SQLite cannot "+
			"add one to an existing table here. Nothing in it is lost -- it is all "+
			"derived from the exports -- so point -db at a new file and import the "+
			"CSV again", path, stale, requiredColumns[stale])
	}
	return false, nil
}

// requiredColumns names one column per table that only the current shape has.
var requiredColumns = map[string]string{
	"series":    "entity",
	"forecasts": "entity",
	"runs":      "as_of",
	"raw":       "data",
}

// staleTable names the first table that exists but is the wrong shape, or "" if
// there is none.
//
// A table that is absent is not evidence of anything: openDB creates the schema
// immediately after this, and a second process opening the same new file can
// arrive midway through those statements and see some tables and not others.
// Treating absence as staleness made a concurrent first open fail intermittently
// with the refusal below.
func staleTable(db *sql.DB) (string, error) {
	for _, table := range []string{"series", "forecasts", "runs", "raw"} {
		var columns, match int
		if err := db.QueryRow(
			`SELECT COUNT(*), COUNT(*) FILTER (WHERE name = ?)
			 FROM pragma_table_info(?)`,
			requiredColumns[table], table).Scan(&columns, &match); err != nil {
			return "", fmt.Errorf("inspecting table %s: %w", table, err)
		}
		if columns > 0 && match == 0 {
			return table, nil
		}
	}
	return "", nil
}

// Point is one observation: a day and a number.
type Point struct {
	Day   string
	Value float64
}

// saveData stores every numeric column, forecast or not, so a run can be explained.
func saveData(db *sql.DB, seriesID string, d *Data) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Replace the whole dataset, the way saveRaw replaces its source.
	//
	// An upsert only touches the cells the new file covers, so a narrower or
	// different export left everything it did not mention behind: a campaign
	// dropped from a later export, days trimmed off the start, or an import for
	// an entirely different account under the same name. `raw` was then exactly
	// the newest file while `series` was the union of every import ever done, and
	// since forecast_accuracy joins to `series` and not to `raw`, those disowned
	// rows kept being scored as though they were actuals. Measured before this:
	// seven days scored against numbers present in no raw row.
	//
	// Deleting first makes the two tables agree by construction. Nothing
	// references `series`, so runs and forecasts are untouched -- a forecast made
	// against data you have since replaced simply stops having an actual to score
	// against, which is the honest outcome.
	if _, err := tx.Exec(`DELETE FROM series WHERE series_id=?`, seriesID); err != nil {
		return fmt.Errorf("clearing previous data for %q: %w", seriesID, err)
	}

	st, err := tx.Prepare(`INSERT INTO series (series_id, entity, metric, day, value)
	                       VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()

	for entity, metrics := range d.Values {
		for name, col := range metrics {
			for i, v := range col {
				if _, err := st.Exec(seriesID, entity, name, d.Days[i], v); err != nil {
					return fmt.Errorf("saving %s/%s/%s %s: %w",
						seriesID, entity, name, d.Days[i], err)
				}
			}
		}
	}
	return tx.Commit()
}

// saveRaw stores every input row verbatim, replacing any previous import of the
// same source so re-importing a corrected export does not leave stale rows behind.
func saveRaw(db *sql.DB, source string, rows []RawRow) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM raw WHERE source=?`, source); err != nil {
		return fmt.Errorf("clearing previous import of %q: %w", source, err)
	}
	st, err := tx.Prepare(`INSERT INTO raw (source, row_num, day, data) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		blob, err := json.Marshal(r.Data)
		if err != nil {
			return fmt.Errorf("line %d: %w", r.Line, err)
		}
		if _, err := st.Exec(source, r.Line, r.Day, string(blob)); err != nil {
			return fmt.Errorf("saving raw line %d: %w", r.Line, err)
		}
	}
	return tx.Commit()
}

func loadSeries(db *sql.DB, seriesID, entity, metric string) ([]Point, error) {
	rows, err := db.Query(`SELECT day, value FROM series
	                       WHERE series_id=? AND entity=? AND metric=? ORDER BY day`,
		seriesID, entity, metric)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Day, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Run is one forecast, and everything needed to reproduce and explain it.
type Run struct {
	ID        string
	SeriesID  string
	Model     string
	Horizon   int
	Metrics   []string
	Entities  []string
	GroupBy   string
	AsOf      string // last day of real data the forecast was based on
	CreatedAt time.Time
	InputHash string
	ModelInfo json.RawMessage // the worker's handshake, verbatim
}

// saveRun stores one run and every entity's forecast.
// values is entity -> [metric][day][quantile].
func saveRun(db *sql.DB, r Run, days []string, quantiles []float64,
	values map[string][][][]float64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO runs
	    (id, series_id, model, horizon, created_at, as_of, metrics, entities, group_by,
	     input_sha256, model_info)
	    VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.SeriesID, r.Model, r.Horizon, r.CreatedAt.UTC().Format(time.RFC3339), r.AsOf,
		strings.Join(r.Metrics, ","), strings.Join(r.Entities, "\x1f"), r.GroupBy,
		r.InputHash, string(r.ModelInfo))
	if err != nil {
		return fmt.Errorf("saving run: %w", err)
	}

	st, err := tx.Prepare(`INSERT INTO forecasts (run_id, entity, metric, day, quantile, value)
	                       VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, entity := range r.Entities {
		per, ok := values[entity]
		if !ok {
			return fmt.Errorf("no forecast for entity %q", entity)
		}
		for m, metric := range r.Metrics {
			for i, day := range days {
				for j, q := range quantiles {
					if _, err := st.Exec(r.ID, entity, metric, day, q, per[m][i][j]); err != nil {
						return fmt.Errorf("saving forecast %s/%s/%s q%.2f: %w",
							entity, metric, day, q, err)
					}
				}
			}
		}
	}
	return tx.Commit()
}

// hashInput fingerprints the exact numbers sent to a model, so two runs can be
// compared without storing the input twice.
func hashInput(days []string, entities, metrics []string,
	values map[string]map[string][]float64) string {
	h := sha256.New()
	for _, e := range entities {
		for _, name := range metrics {
			fmt.Fprintf(h, "#%s/%s\n", e, name)
			for i, day := range days {
				fmt.Fprintf(h, "%s=%.10g\n", day, values[e][name][i])
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
