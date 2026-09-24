package main

// Round 29: the SQLite setup itself, audited against .claude/skills/sqlite-optimization.
// Every test here corresponds to a defect that was present and is now fixed, so a
// regression reintroduces a failure rather than a silent slowdown or a bad number.

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The pragmas that matter are per-connection, so they have to be set in the DSN.
// foreign_keys in particular defaults to off, which left the run_id reference in
// forecasts declared but unenforced.
func TestConnectionPragmas(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, want := range []struct{ pragma, value string }{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "10000"},
		{"synchronous", "2"}, // FULL, stated in openDB as a deliberate choice
		{"user_version", fmt.Sprint(schemaVersion)},
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + want.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", want.pragma, err)
		}
		if got != want.value {
			t.Errorf("PRAGMA %s = %s, want %s", want.pragma, got, want.value)
		}
	}
}

// With foreign_keys on, a forecast cannot reference a run that does not exist.
// Before, this inserted happily and left a row nothing could explain.
func TestForeignKeyEnforced(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO forecasts VALUES ('no-such-run','(account)','Cost','2026-01-01',0.5,1)`)
	if err == nil {
		t.Fatal("inserted a forecast for a run that does not exist")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}
}

// A database from before per-entity storage must be refused with an explanation,
// not left to fail later on a bare "no such column: entity". CREATE TABLE IF NOT
// EXISTS does not migrate it, and this is the only thing standing in for that.
func TestOldSchemaRefusedWithExplanation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// The shape this project actually had before campaigns: no entity anywhere.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE series (series_id TEXT, metric TEXT, day TEXT, value REAL,
		    PRIMARY KEY (series_id, metric, day));
		CREATE TABLE runs (id TEXT PRIMARY KEY, series_id TEXT, model TEXT,
		    horizon INTEGER, created_at TEXT, metrics TEXT, input_sha256 TEXT,
		    model_info TEXT);
		CREATE TABLE forecasts (run_id TEXT, metric TEXT, day TEXT, quantile REAL,
		    value REAL, PRIMARY KEY (run_id, metric, day, quantile));`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := openDB(path)
	if err == nil {
		db.Close()
		t.Fatal("opened a pre-entity database instead of refusing it")
	}
	for _, want := range []string{"entity", "import the CSV again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// A current-shaped file made before stamping began carries user_version 0. It is
// fine, and must be adopted rather than refused.
func TestUnstampedCurrentSchemaIsAdopted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unstamped.db")

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = openDB(path)
	if err != nil {
		t.Fatalf("refused a current-shaped database that simply lacked a stamp: %v", err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d after adoption, want %d", v, schemaVersion)
	}
}

// A file from a future build must be refused too, rather than half-read.
func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if db, err := openDB(path); err == nil {
		db.Close()
		t.Fatal("opened a database written by a newer schema")
	} else if !strings.Contains(err.Error(), "newer version") {
		t.Errorf("refusal does not explain the version gap: %v", err)
	}
}

// The database carries the account's spend history and must not be left
// world-readable by the default umask.
func TestDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perm.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("database is %04o, want no group or other access", mode)
	}
}

// Every query through forecast_accuracy filters on quantile, which is the last
// column of the primary key and so unsearchable without this index. The plan is
// the assertion: a SCAN of forecasts here is the 1.6s regression coming back.
func TestAccuracyQueryUsesAnIndex(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, q := range []string{
		`SELECT COUNT(*) FROM forecast_accuracy WHERE entity='(account)'`,
		`SELECT DISTINCT entity FROM forecasts f JOIN runs r ON r.id=f.run_id
		 WHERE f.quantile = 0.5`,
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), "forecasts_median") {
			t.Errorf("query does not use forecasts_median:\n%s\nplan:\n%s", q, plan.String())
		}
		if strings.Contains(plan.String(), "SCAN forecasts") {
			t.Errorf("query scans forecasts:\n%s\nplan:\n%s", q, plan.String())
		}
	}
}

// known() interpolates its column name, because a placeholder cannot bind an
// identifier. It must refuse anything outside the closed list.
func TestKnownRefusesUnlistedColumn(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, col := range []string{"value", "1", "entity)--", "run_id"} {
		if err := known(db, col, "things", "x", ""); err == nil {
			t.Errorf("known accepted column %q", col)
		}
	}
	// The two real ones still work: nothing stored, so any value is unknown, but
	// the failure must be about the value rather than the column.
	for _, col := range []string{"entity", "metric"} {
		if err := known(db, col, "things", "x", ""); err != nil &&
			strings.Contains(err.Error(), "refusing to query column") {
			t.Errorf("known rejected the legitimate column %q", col)
		}
	}
}

// strconv.ParseFloat accepts "NaN", "Inf" and "Infinity". NaN then violates
// value NOT NULL and surfaces as a raw constraint error; Inf is stored silently
// and every sum, axis and average downstream is wrong from then on.
func TestNonFiniteCsvValuesRejected(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"NaN", "nan", "Inf", "-Inf", "Infinity", "+inf"} {
		path := filepath.Join(dir, "x.csv")
		if err := os.WriteFile(path, []byte(fullLengthCSV(bad)), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := readCSV(path, nil, "")
		if err != nil {
			if !strings.Contains(err.Error(), "finite") {
				t.Errorf("%q rejected for the wrong reason: %v", bad, err)
			}
			continue
		}
		for name, col := range d.Values[AccountEntity] {
			for _, v := range col {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("%q was accepted and stored as %v in %s", bad, v, name)
				}
			}
		}
		t.Errorf("%q was accepted rather than refused", bad)
	}
}

// SQLite turns a NaN bind into NULL, so value NOT NULL is what actually stops it
// reaching storage. Inf is not caught by anything in the database, which is why
// ingest has to. This pins both behaviours, since the guard above is only correct
// while they hold.
func TestStorageRejectsNaNButAcceptsInf(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "nf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO series VALUES ('s','(account)','Cost','2026-01-01',?)`,
		math.NaN()); err == nil {
		t.Error("SQLite stored NaN in a NOT NULL REAL column")
	}
	if _, err := db.Exec(
		`INSERT INTO series VALUES ('s','(account)','Cost','2026-01-02',?)`,
		math.Inf(1)); err != nil {
		t.Errorf("expected SQLite to accept Inf, so ingest must be the guard: %v", err)
	}
}

// The quantile columns are REAL and the view joins them with "= 0.1", an exact
// float comparison. It holds only because both workers send decimal literals
// that parse to the same double the view uses. A model that computed its levels
// instead would silently produce NULL low/high and a NULL inside_range, so this
// records the dependency.
func TestQuantileJoinIsExactFloatMatch(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT 0.1 AS q) WHERE q = ?`,
		0.1).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("0.1 from Go does not equal 0.1 in SQL; the view's quantile joins cannot work")
	}
	// Computed at runtime on purpose: Go folds a constant 0.1+0.2 with exact
	// precision, producing 0.3 and proving nothing.
	tenth, fifth := 0.1, 0.2
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT 0.3 AS q) WHERE q = ?`,
		tenth+fifth).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a drifted float matched an exact literal; this test no longer proves anything")
	}
}

// fullLengthCSV builds a file long enough to reach value parsing. A short one is
// refused for its length first, which is how the fuzz corpus's NaN and Inf
// fragments went three rounds without ever testing what they were meant to.
func fullLengthCSV(value string) string {
	var b strings.Builder
	b.WriteString("Day,Cost,Clicks\n")
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < smallestUsefulSeries+8; i++ {
		v := "100"
		if i == 10 {
			v = value
		}
		fmt.Fprintf(&b, "%s,%s,5\n", day.AddDate(0, 0, i).Format("2006-01-02"), v)
	}
	return b.String()
}

// A second process opening the same new file can arrive while the first is midway
// through the schema statements, with some tables created and others not. Treating
// an absent table as evidence of an old schema made that concurrent first open
// fail intermittently, which is worse than failing every time.
func TestPartialSchemaNotMistakenForOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Only the first table the schema creates.
	if _, err := raw.Exec(`CREATE TABLE raw (source TEXT NOT NULL, row_num INTEGER
		NOT NULL, day TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY (source,row_num))`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("refused a half-created database as though it were an old one: %v", err)
	}
	db.Close()
}

// The refusal must name the table it actually objected to, so the message is
// useful on a file nobody remembers creating.
func TestStaleTableIsNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "named.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE runs (id TEXT PRIMARY KEY, series_id TEXT,
		model TEXT, horizon INTEGER, created_at TEXT, metrics TEXT,
		input_sha256 TEXT, model_info TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	if db, err := openDB(path); err == nil {
		db.Close()
		t.Fatal("accepted a runs table with no as_of column")
	} else if !strings.Contains(err.Error(), "runs table has no as_of column") {
		t.Errorf("refusal does not name the table and column: %v", err)
	}
}

// Opening an already-stamped database must write nothing, so a database on
// read-only media can still be queried. Gating the schema on user_version
// briefly made every open -- including a pure read like accuracy -- attempt a
// write, which failed outright on a read-only file.
func TestReadOnlyDatabaseCanBeQueried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO series VALUES ('s','(account)','Cost','2026-01-01',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// -wal and -shm must be gone, or SQLite needs to write them on the next open.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Skipf("%s still present after close; WAL cannot be read-only", suffix)
		}
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)

	db, err = openDB(path)
	if err != nil {
		t.Fatalf("could not open a read-only database: %v", err)
	}
	defer db.Close()

	pts, err := loadSeries(db, "s", AccountEntity, "Cost")
	if err != nil {
		t.Fatalf("could not read from a read-only database: %v", err)
	}
	if len(pts) != 1 {
		t.Errorf("read %d points, want 1", len(pts))
	}
}

// SQLite accepts several -db values that do not mean what the user intended. An
// empty one is the dangerous case: it opens an anonymous temporary database, so a
// forecast printed "saved run ... to " and stored nothing at all.
func TestUnusableDatabasePathsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ path, want string }{
		{"", "no database file given"},
		{"   ", "no database file given"},
		{dir, "is a directory"},
		{filepath.Join(dir, "nope", "x.db"), "does not exist"},
	} {
		db, err := openDB(c.path)
		if err == nil {
			db.Close()
			t.Errorf("openDB(%q) succeeded", c.path)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("openDB(%q): %v, want mention of %q", c.path, err, c.want)
		}
	}
}

// The -db value is interpolated into a SQLite URI, where "?" would start query
// parameters and could turn pragmas back off. PathEscape is what stops that, so a
// path containing "?" must become a file of that name and nothing more.
func TestDatabasePathCannotInjectDsnParameters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db?_pragma=foreign_keys(0)&mode=ro")

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var fk string
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != "1" {
		t.Errorf("a path turned foreign_keys off: %s", fk)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("path was not treated as a literal filename: %v", err)
	}
}

// -entities may exclude the account, and then there is no account total to print.
// The console summary indexed it unconditionally and panicked with "index out of
// range [0] with length 0" on every single-campaign forecast. The report template
// had already been fixed for this; the summary beside it had not.
func TestHeadlineEntity(t *testing.T) {
	for _, c := range []struct {
		name   string
		chosen []string
		want   string
	}{
		{"account present", []string{AccountEntity, "Brand"}, AccountEntity},
		{"account not first", []string{"Brand", AccountEntity}, AccountEntity},
		{"one campaign only", []string{"Performance Max"}, "Performance Max"},
		{"campaigns only", []string{"Brand", "Performance Max"}, "Brand"},
	} {
		if got := headlineEntity(c.chosen); got != c.want {
			t.Errorf("%s: headlineEntity(%v) = %q, want %q", c.name, c.chosen, got, c.want)
		}
	}
}

// A stale view or index is invisible to staleTable -- a view has no columns of
// its own to miss -- so a file carrying an old definition used to be adopted in
// silence and keep it. A deliberately broken forecast_accuracy made `accuracy`
// exit 0 reporting "no forecast day has an actual yet" on a database full of
// scorable rows. The derived objects are compared on every open now.
func TestStaleViewAndIndexAreRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Replace both with something the current code did not write, exactly as an
	// older release would have left them.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP VIEW forecast_accuracy`,
		`CREATE VIEW forecast_accuracy AS SELECT 'stale' AS series_id`,
		`DROP INDEX forecasts_median`,
		`CREATE INDEX forecasts_median ON forecasts (run_id)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()

	// Opening it again must put both back, without needing schemaVersion bumped.
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, o := range derivedObjects {
		var got string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, o.name).Scan(&got); err != nil {
			t.Fatalf("%s is missing after open: %v", o.name, err)
		}
		if strings.TrimSpace(got) != strings.TrimSpace(o.ddl) {
			t.Errorf("%s was not rebuilt; file still has:\n%s", o.name, got)
		}
	}

	// And the rebuilt view must actually be the accuracy view, not a stub.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('forecast_accuracy')
	                       WHERE name IN ('trained_on','inside_range','actual')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("the rebuilt view has %d of its 3 key columns", n)
	}
}

// Opening an unchanged database must stay a pure read: that is what lets a
// read-only file still be queried, and refreshDerived runs on every open.
func TestRefreshingDerivedObjectsWritesNothingWhenCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	db, err = openDB(path)
	if err != nil {
		t.Fatalf("a current, read-only database must still open: %v", err)
	}
	db.Close()
}
