package main

// predictmarketing -- forecast a series of daily numbers with TimesFM 3.0 or
// Chronos-2, store the result, and write a chart you can open in a browser.
//
//   predictmarketing setup
//   predictmarketing models
//   predictmarketing forecast data.csv -model chronos2 -horizon 7
//   predictmarketing runs

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "setup":
		err = cmdSetup()
	case "models":
		err = cmdModels()
	case "import":
		err = cmdImport(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "forecast":
		err = cmdForecast(os.Args[2:])
	case "runs":
		err = cmdRuns(os.Args[2:])
	case "accuracy":
		err = cmdAccuracy(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	case "-v", "--version", "version":
		printVersion(os.Stdout)
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() { usageTo(os.Stderr) }

// usageTo exists so a test can read the help and check it against the flags the
// program actually accepts.
func usageTo(w io.Writer) {
	fmt.Fprintf(w, `predictmarketing -- forecasting with TimesFM 3.0 or Chronos-2

  setup                          download model weights and verify them
  models                         show each model and what it can do
  import [options]               read new CSVs from data/, forecast with every model
  report [options]               redraw the reports from what is already stored
  forecast FILE.csv [options]    forecast a CSV of date,value rows
  runs                           list past forecasts
  accuracy                       compare past forecasts with what actually happened
  version                        what this build is, for bug reports

forecast options:
  -model NAME     which model (%s)
  -horizon N      days ahead (default 7)
  -history N      days of past data drawn on the chart (default 90)
  -columns A,B    which columns to forecast (default: every column of numbers)
  -entities A;B   which campaigns, separated by ; (default: all, plus the account
                  total). Semicolons, because campaign names contain commas.
  -by NAME        the column that separates campaigns, if it cannot be worked out.
                  A name, not a measurement: a column that is forecast is refused
  -future K=V,V   known-future values, e.g. -future budget=500,500,600
  -series NAME    name for this series (default: the file name)
  -out FILE       HTML report path (default: alongside the CSV)
  -db FILE        database file (default: pm.db)

import options:
  -data DIR       folder to read CSVs from (default data)
  -horizon N      days ahead (default 7)
  -history N      days of past data drawn on the charts (0, the default, is all
                  of them -- the chart scrolls)
  -no-finetune    write only the first report, skipping the trained model
  -db FILE        database file (default: pm.db)

report options:
  -series NAME    which dataset (default: every one with forecasts)
  -history N      days of past data drawn on the charts (0, the default, is all)
  -data DIR       folder whose reports/ to write into (default data)
  -db FILE        database file (default: pm.db)

accuracy options:
  -entity NAME    one campaign (default: the account total)
  -metric NAME    one metric (default: all)
  -series NAME    one dataset (default: all)
  -by-day         break the numbers down by how far ahead they were forecast
  -db FILE        database file (default: pm.db)
`, strings.Join(sortedNames(), ", "))
}

// printVersion reports what this build actually is.
//
// Nothing is hardcoded: the module version and the commit come from the build
// information Go embeds, so they cannot drift from the binary the way a constant
// would. A checkout that is not a repository has no revision to report, and says
// so rather than inventing one.
func printVersion(w io.Writer) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(w, "predictmarketing (no build information available)")
		return
	}
	version := info.Main.Version
	if version == "" || version == "(devel)" {
		version = "development build"
	}
	revision, modified := "", false
	for _, set := range info.Settings {
		switch set.Key {
		case "vcs.revision":
			revision = set.Value
		case "vcs.modified":
			modified = set.Value == "true"
		}
	}
	fmt.Fprintf(w, "predictmarketing %s\n", version)
	fmt.Fprintf(w, "  module   %s\n", info.Main.Path)
	if revision != "" {
		dirty := ""
		if modified {
			dirty = " (with uncommitted changes)"
		}
		fmt.Fprintf(w, "  commit   %s%s\n", short(revision), dirty)
	} else {
		fmt.Fprintln(w, "  commit   unknown -- built outside a repository")
	}
	fmt.Fprintf(w, "  go       %s\n", info.GoVersion)
	fmt.Fprintln(w, "\nModel versions and weight checksums: predictmarketing models")
}

func sortedNames() []string {
	n := modelNames()
	sort.Strings(n)
	return n
}

func cmdSetup() error {
	root := installDir()
	if root == "" {
		return fmt.Errorf("cannot find the models folder next to the binary or here")
	}
	python := venvPython(root)
	if _, err := os.Stat(python); err != nil {
		return fmt.Errorf("no Python environment at %s\n"+
			"create it with:\n  uv venv models/.venv --python 3.11\n"+
			"  VIRTUAL_ENV=$PWD/models/.venv uv pip install -r models/requirements.txt", python)
	}
	cmd := exec.Command(python, filepath.Join(root, "models", "fetch.py"))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"HF_HOME="+mustAbs(filepath.Join(root, "models", "cache")),
		"PYTHONDONTWRITEBYTECODE=1")
	return cmd.Run()
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func cmdModels() error {
	for _, name := range sortedNames() {
		w, err := startWorker(name)
		if err != nil {
			fmt.Printf("%-10s unavailable: %v\n", name, err)
			continue
		}
		s := w.Shake
		fmt.Printf("%-10s %s @ %s\n", name, s.Repo, short(s.Revision))
		fmt.Printf("           known-future values: %s\n", yesNo(s.Covariates))
		fmt.Printf("           quantiles          : %v\n", s.Quantiles)
		fmt.Printf("           weights sha256     : %s\n", short(s.WeightsSHA256))
		fmt.Printf("           versions           : %v\n", s.Versions)
		w.Close()
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func cmdForecast(args []string) error {
	fs := flag.NewFlagSet("forecast", flag.ExitOnError)
	model := fs.String("model", "chronos2", "which model")
	horizon := fs.Int("horizon", 7, "days ahead")
	series := fs.String("series", "", "name for this series")
	columns := fs.String("columns", "", "which columns to forecast (default: every column holding numbers)")
	by := fs.String("by", "", "column that separates campaigns -- a name, not a "+
		"measurement (default: worked out from the file)")
	entities := fs.String("entities", "", "which campaigns to forecast, separated by ; (default: all, plus the account total)")
	future := fs.String("future", "", "known-future values, e.g. budget=500,500,600")
	history := fs.Int("history", 90, "days of past data to draw on the chart")
	out := fs.String("out", "", "HTML report path")
	dbPath := fs.String("db", defaultPath("pm.db"), "database file")
	fs.Parse(reorderFlags(args))

	if fs.NArg() < 1 {
		return fmt.Errorf("need a CSV file\n  predictmarketing forecast data.csv -model chronos2")
	}
	// One file per run. Taking the first and ignoring the rest is the wrong kind
	// of quiet: `forecast *.csv` would forecast one file and say nothing about the
	// others, and the report would look perfectly normal.
	if fs.NArg() > 1 {
		return fmt.Errorf("one CSV at a time, but %d were given (%s).\n"+
			"If a wildcard expanded to all of them, name the one you want",
			fs.NArg(), strings.Join(fs.Args(), ", "))
	}
	csvPath := fs.Arg(0)
	if *horizon < 1 {
		return fmt.Errorf("horizon must be at least 1, got %d", *horizon)
	}

	data, err := readCSV(csvPath, splitList(*columns), *by)
	if err != nil {
		return err
	}
	name := *series
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(csvPath), filepath.Ext(csvPath))
	}

	fut, err := parseFuture(*future, *horizon)
	if err != nil {
		return err
	}
	// A known-future column need only be a number that is stored -- it does not
	// have to be forecastable. Budget is the obvious case: it is a setting, so it
	// is never forecast, and it is exactly the thing you know in advance because
	// you chose it.
	for fname := range fut {
		if _, ok := data.Values[AccountEntity][fname]; !ok {
			return fmt.Errorf("-future %s: no numeric column named %q in %s (columns: %s)",
				fname, fname, csvPath,
				strings.Join(append(append([]string{}, data.Names...), data.Settings...), ", "))
		}
	}

	// A column whose future you supply is an input to the forecast, not one of
	// the things being forecast. Everything else is a metric, and both models
	// predict them together in one call.
	metrics := make([]string, 0, len(data.Names))
	for _, n := range data.Names {
		if _, isInput := fut[n]; !isInput {
			metrics = append(metrics, n)
		}
	}
	if len(metrics) == 0 {
		return fmt.Errorf("every column was given as a known-future input, so there is "+
			"nothing left to forecast (columns: %s)", strings.Join(data.Names, ", "))
	}

	// Check where the report will go before spending time on a forecast.
	// The model goes in the default file name. Without it both models write to
	// the same file, so running one after the other silently destroys the first
	// report -- exactly when you are trying to compare them.
	htmlPath := *out
	if htmlPath == "" {
		htmlPath = strings.TrimSuffix(csvPath, filepath.Ext(csvPath)) +
			"_forecast_" + *model + ".html"
	}
	if dir := filepath.Dir(htmlPath); dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("cannot write the report to %s: %w", htmlPath, err)
		}
	}

	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := saveData(db, name, data); err != nil {
		return fmt.Errorf("writing to %s: %w", *dbPath, err)
	}
	// Keep every input row too. The aggregated series loses the text columns and
	// the per-campaign detail; this is the history to come back to later.
	if err := saveRaw(db, name, data.Raw); err != nil {
		return fmt.Errorf("writing raw rows to %s: %w", *dbPath, err)
	}

	w, err := startWorker(*model)
	if err != nil {
		return err
	}
	defer w.Close()

	// Which entities to forecast: the account total, and each campaign.
	wantEntities := splitOn(*entities, ";") // campaign names contain commas
	chosen := data.Entities
	if len(wantEntities) > 0 {
		chosen = nil
		for _, e := range wantEntities {
			if strings.EqualFold(e, "account") {
				e = AccountEntity
			}
			found := ""
			for _, have := range data.Entities {
				if strings.EqualFold(have, e) {
					found = have
					break
				}
			}
			if found == "" {
				// It may be in the file and simply have had nothing to forecast.
				// Saying "no campaign named" about a campaign the reader can see in
				// their own export sends them hunting for a typo that is not there.
				for _, idle := range data.Inactive {
					if strings.EqualFold(idle, e) {
						return fmt.Errorf("-entities: %q is in the file, and stored, but %s",
							idle, whyNotForecast(data, idle))
					}
				}
				return fmt.Errorf("-entities: no campaign named %q (have: %s)",
					e, strings.Join(data.Entities, ", "))
			}
			chosen = append(chosen, found)
		}
	}

	fmt.Printf("%s: %d days of %q -> %d days ahead\n", *model, len(data.Days), name, *horizon)
	if data.RowsPerDay > 1 {
		fmt.Printf("  %d rows per day, split by %q (%d rows kept in the raw table)\n",
			data.RowsPerDay, data.GroupBy, len(data.Raw))
	}
	fmt.Printf("  forecasting: %s\n", strings.Join(metrics, ", "))
	fmt.Printf("  for %d: %s\n", len(chosen), strings.Join(chosen, ", "))
	if len(fut) > 0 {
		fmt.Printf("  using known-future: %s\n", strings.Join(sortedKeys(fut), ", "))
	}
	for _, line := range exclusionLines(data) {
		fmt.Println(line)
	}
	if len(data.Identifiers) > 0 {
		fmt.Printf("  not forecast, look like identifiers: %s\n",
			strings.Join(data.Identifiers, ", "))
	}
	if len(data.Settings) > 0 {
		fmt.Printf("  stored, not forecast (you set these, you do not predict them): %s\n",
			strings.Join(data.Settings, ", "))
	}
	if len(data.Averaged) > 0 {
		fmt.Printf("  rates, averaged across campaigns for the account figure: %s\n",
			strings.Join(data.Averaged, ", "))
	}
	if len(data.Skipped) > 0 {
		fmt.Printf("  stored but not numbers: %s\n", strings.Join(data.Skipped, ", "))
	}

	// One request per entity, all on the same model process.
	forecasts := map[string][][][]float64{}
	worst := 0.0
	for _, entity := range chosen {
		series := make([][]float64, len(metrics))
		for i, n := range metrics {
			series[i] = data.Values[entity][n]
		}
		q, err := w.Forecast(series, metrics, *horizon, w.Shake.Quantiles,
			data.Values[entity], fut)
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		forecasts[entity] = q
		if w.LastCrossing > worst {
			worst = w.LastCrossing
		}
	}
	if worst > 0 {
		fmt.Printf("  note: the model returned quantiles slightly out of order "+
			"(largest %.3f%%); they were sorted back into order\n", worst*100)
	}

	days, err := nextDays(data.Days[len(data.Days)-1], *horizon)
	if err != nil {
		return err
	}
	run := Run{ID: newID(), SeriesID: name, Model: *model, Horizon: *horizon,
		Metrics: metrics, Entities: chosen, GroupBy: data.GroupBy,
		AsOf:      data.Days[len(data.Days)-1],
		CreatedAt: time.Now(),
		InputHash: hashInput(data.Days, chosen, metrics, data.Values), ModelInfo: w.Raw}
	if err := saveRun(db, run, days, w.Shake.Quantiles, forecasts); err != nil {
		return err
	}
	if err := writeReport(htmlPath, run, data, days, w.Shake.Quantiles, forecasts, *history); err != nil {
		return err
	}

	headline := headlineEntity(chosen)
	mid := len(w.Shake.Quantiles) / 2
	for mi, metric := range metrics {
		fmt.Printf("\n  %s -- %s\n", headline, metric)
		fmt.Printf("  %-12s %14s %14s %14s\n", "day", "low", "median", "high")
		pct := data.Percent[metric]
		for i, d := range days {
			row := forecasts[headline][mi][i]
			fmt.Printf("  %-12s %14s %14s %14s\n", d,
				formatMetric(row[0], pct), formatMetric(row[mid], pct),
				formatMetric(row[len(row)-1], pct))
		}
	}
	if len(chosen) > 1 {
		fmt.Printf("\n  next %d days per campaign, %s total (median)\n", *horizon, metrics[0])
		for _, e := range chosen {
			if e == AccountEntity {
				continue
			}
			total := 0.0
			for i := range days {
				total += forecasts[e][0][i][mid]
			}
			fmt.Printf("  %-42s %14s\n", truncate(e, 42), formatValue(total))
		}
	}
	fmt.Printf("\nsaved run %s to %s\nreport: %s\n", short(run.ID), *dbPath, htmlPath)
	return nil
}
func parseFuture(s string, horizon int) (map[string][]float64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string][]float64{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return nil, fmt.Errorf("-future %q: expected NAME=v1,v2,...", part)
		}
		var vals []float64
		for _, n := range strings.Split(v, ",") {
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil {
				return nil, fmt.Errorf("-future %s: %q is not a number", k, n)
			}
			vals = append(vals, f)
		}
		if len(vals) != horizon {
			return nil, fmt.Errorf("-future %s has %d values but the horizon is %d",
				k, len(vals), horizon)
		}
		k = strings.TrimSpace(k)
		// Quietly keeping the last of two entries for one column would discard
		// numbers the user typed.
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("-future names %q twice; give it once", k)
		}
		out[k] = vals
	}
	return out, nil
}

// reorderFlags moves flags ahead of file names.
//
// Go's flag package stops at the first non-flag argument, so
// `forecast data.csv -model timesfm3` would silently ignore -model and use the
// default -- running a different model than the one asked for. That is exactly
// the kind of quiet wrongness this tool is supposed to refuse, so fix the parse
// rather than document the footgun.
//
// ponytail: assumes every flag takes a value, which holds because `forecast` has
// no boolean flags. Add one and this needs to consult the FlagSet.
func reorderFlags(args []string) []string {
	var flags, files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			files = append(files, a)
			continue
		}
		flags = append(flags, a)
		if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, files...)
}

func cmdRuns(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	dbPath := fs.String("db", defaultPath("pm.db"), "database file")
	fs.Parse(args)

	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, series_id, model, horizon, created_at, model_info
	                       FROM runs ORDER BY created_at DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("%-14s %-20s %-10s %-8s %-22s %s\n", "RUN", "SERIES", "MODEL", "HORIZON", "WHEN", "WEIGHTS")
	for rows.Next() {
		var id, sid, model, when, info string
		var h int
		if err := rows.Scan(&id, &sid, &model, &h, &when, &info); err != nil {
			return err
		}
		var hs Handshake
		json.Unmarshal([]byte(info), &hs)
		fmt.Printf("%-14s %-20s %-10s %-8d %-22s %s\n",
			short(id), sid, model, h, when, short(hs.WeightsSHA256))
	}
	return rows.Err()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "\u2026"
}

func splitList(s string) []string { return splitOn(s, ",") }

// splitOn exists because campaign names routinely contain commas -- "Video
// Efficient Reach, Infeed, 9-16-25, CPM" is a real one -- so -entities is
// separated by semicolons instead, matching -future.
func splitOn(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sortedKeys(m map[string][]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmdAccuracy compares stored forecasts with the actuals that arrived later.
//
// Nothing is recomputed: the forecasts were saved when they were made, and the
// actuals appear as newer exports are imported. A day that has not happened yet
// simply has no actual, and is counted as still waiting.
func cmdAccuracy(args []string) error {
	fs := flag.NewFlagSet("accuracy", flag.ExitOnError)
	dbPath := fs.String("db", defaultPath("pm.db"), "database file")
	entity := fs.String("entity", AccountEntity, "which campaign (default: the account total)")
	metric := fs.String("metric", "", "one metric (default: all)")
	series := fs.String("series", "", "which dataset (default: all)")
	byDay := fs.Bool("by-day", false, "break the numbers down by days ahead")
	fs.Parse(reorderFlags(args))

	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	where := "WHERE entity=?"
	arg := []any{*entity}
	if *metric != "" {
		where += " AND metric=?"
		arg = append(arg, *metric)
	}
	if *series != "" {
		where += " AND series_id=?"
		arg = append(arg, *series)
	}

	// A name that matches nothing should say so, not return an empty table that
	// reads like "no data yet".
	if err := known(db, "entity", "campaigns", *entity, *series); err != nil {
		return err
	}
	if *metric != "" {
		if err := known(db, "metric", "metrics", *metric, *series); err != nil {
			return err
		}
	}

	var waiting int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forecast_accuracy `+where+
		` AND actual IS NULL AND trained_on = 0`, arg...).Scan(&waiting); err != nil {
		return err
	}
	// Days a fine-tuned model was trained on are excluded everywhere below. Count
	// them so the exclusion is visible rather than silent.
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forecast_accuracy `+where+
		` AND actual IS NOT NULL AND trained_on = 1`, arg...).Scan(&leaked); err != nil {
		return err
	}

	group, head := "model, metric", "%-10s %-14s"
	if *byDay {
		group, head = "model, metric, days_ahead", "%-10s %-14s"
	}
	q := `SELECT model, metric, ` +
		map[bool]string{true: "days_ahead", false: "0"}[*byDay] + `,
		       COUNT(*), AVG(ABS(pct_error)), AVG(pct_error), AVG(inside_range)*100
		FROM forecast_accuracy ` + where + ` AND actual IS NOT NULL AND trained_on = 0
		GROUP BY ` + group + ` ORDER BY ` + group
	rows, err := db.Query(q, arg...)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("forecast vs actual for %s\n\n", *entity)
	if *byDay {
		fmt.Printf("  "+head+" %5s %8s %10s %9s %9s\n",
			"model", "metric", "ahead", "days", "avg error", "bias", "in range")
	} else {
		fmt.Printf("  "+head+" %8s %10s %9s %9s\n",
			"model", "metric", "days", "avg error", "bias", "in range")
	}

	any := false
	for rows.Next() {
		var model, met string
		var ahead, n int
		var mape, bias, inside sql.NullFloat64
		if err := rows.Scan(&model, &met, &ahead, &n, &mape, &bias, &inside); err != nil {
			return err
		}
		any = true
		if *byDay {
			fmt.Printf("  %-10s %-14s %5d %8d %9.1f%% %+8.1f%% %8.0f%%\n",
				model, met, ahead, n, mape.Float64, bias.Float64, inside.Float64)
		} else {
			fmt.Printf("  %-10s %-14s %8d %9.1f%% %+8.1f%% %8.0f%%\n",
				model, met, n, mape.Float64, bias.Float64, inside.Float64)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !any {
		fmt.Println("  no forecast day has an actual yet.")
	}
	fmt.Printf("\n  %d forecast days are still waiting for their actuals.\n", waiting)
	if leaked > 0 {
		fmt.Printf("  %d days excluded: a fine-tuned model was trained on them, so\n"+
			"  scoring against them would measure memorisation, not forecasting.\n", leaked)
	}
	if any {
		fmt.Println("  avg error is how far off, ignoring direction. bias is the direction:")
		fmt.Println("  positive means the forecast ran high. in range is how often the")
		fmt.Println("  actual landed inside the q10-q90 band, which should be about 80%.")
	}
	return nil
}

// headlineEntity picks the entity the detailed per-metric table describes.
//
// It is the account total whenever that was forecast. -entities can leave the
// account out, though, and then there is no total to show and the first chosen
// campaign is the subject instead. Indexing the account unconditionally panicked
// on every single-campaign run -- the same assumption that had already been
// fixed in the report template, still live in the console summary beside it.
func headlineEntity(chosen []string) string {
	for _, e := range chosen {
		if e == AccountEntity {
			return e
		}
	}
	return chosen[0]
}

// known checks that a filter matches something, and lists the real values if not.
func known(db *sql.DB, column, plural, value, series string) error {
	// column is interpolated, not bound -- SQL placeholders bind values, never
	// identifiers. It is the only identifier this program builds into a statement,
	// so it is checked against a closed list here rather than trusted to callers.
	if column != "entity" && column != "metric" {
		return fmt.Errorf("known: refusing to query column %q", column)
	}
	// Restricting to the median returns exactly the same set of names -- every
	// forecast row set has one -- while reading through forecasts_median instead of
	// scanning every quantile of every run: 1.16s to 1ms on a 2.3M-row file.
	q := `SELECT DISTINCT ` + column + ` FROM forecasts f JOIN runs r ON r.id=f.run_id
	      WHERE f.quantile = 0.5`
	var args []any
	if series != "" {
		q += ` AND r.series_id=?`
		args = append(args, series)
	}
	rows, err := db.Query(q+` ORDER BY 1`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var have []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		if strings.EqualFold(v, value) {
			return rows.Err()
		}
		have = append(have, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(have) == 0 {
		return fmt.Errorf("no forecasts stored yet -- run `predictmarketing forecast` first")
	}
	return fmt.Errorf("-%s %q: not one of the %s forecast (have: %s)",
		column, value, plural, strings.Join(have, " | "))
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
