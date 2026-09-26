package main

// The recurring job, as one command.
//
// You export a CSV, drop it in data/, and run `predictmarketing import`. It
// forecasts with every model, writes the reports, and files the CSV away in
// data/imported/ so the folder only ever holds what has not been read yet.
//
// Two reports come out, because the third model has to be trained on your data
// before it can say anything and that takes minutes rather than seconds:
//
//	report 1  chronos2 + timesfm3            written immediately
//	report 2  chronos2 + timesfm3 + chronos2ft   after the fine-tune finishes
//
// Report 1 is on disk and readable while the fine-tune is still running, which
// is the point of splitting them.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A forecast needs enough history to see a pattern. 32 days is the floor for the
// models to run at all (see smallestUsefulSeries); these are the numbers that
// make the answer worth reading.
const (
	importMinDays  = 90  // refused below this
	importGoodDays = 365 // a year: annual seasonality becomes learnable
	importBestDays = 730 // two years: seasonality can be confirmed, not guessed
)

const (
	importDir    = "data"
	importedName = "imported"
	reportsName  = "reports"
)

// The windows every import forecasts, longest first.
//
// One model on one history is a single opinion. The same model on three
// histories shows whether that opinion depends on how far back you look --
// which, measured on a real export, it mostly does not once the newest day is
// complete, and dramatically does when it is not. Drawing them together is the
// point: where the lines agree you can believe them, and where they separate
// the spread is the honest measure of confidence.
//
// days = 0 means the whole file. A window longer than the file is skipped
// rather than refused, so a short export still produces everything it can.
var importWindows = []struct {
	label string
	days  int
}{
	{"full", 0},
	{"270d", 270},
	{"90d", 90},
}

// averageLabel is the ensemble line: the mean of the pretrained window runs,
// stored as a run of its own so `accuracy` can score it against them.
//
// It deliberately excludes chronos2ft. The fine-tune is fitted to the same data
// it would be averaged into, and it has not beaten the stock models (AGENTS.md
// 4c), so including it would let a weaker, leakier opinion pull the ensemble.
const averageLabel = "average@90d"

// averageWindow is the window the average is built from. Measured over 31
// walk-forward origins on a real account, a 90-day window beat both the whole
// file and 270 days at every one of 7 horizons, at account and campaign level,
// by about 1.7 points of mean absolute error -- roughly 2.5x the difference
// between the two models. The window is the decision that matters.
const averageWindow = "90d"

// runLabel is what goes in runs.model: the model and the window it saw. The
// worker is still started by the bare model name.
func runLabel(model, window string) string { return model + "@" + window }

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dir := fs.String("data", defaultPath(importDir), "folder to read CSVs from")
	horizon := fs.Int("horizon", 7, "days ahead")
	history := fs.Int("history", 0, "days of past data drawn on the charts (0 = all of it)")
	dbPath := fs.String("db", defaultPath("pm.db"), "database file")
	skipFinetune := fs.Bool("no-finetune", false,
		"skip the fine-tuned model and write only the first report")
	fillAbsent := fs.Bool("fill-absent", false, "for exports that list a campaign only "+
		"on the days it ran: add zero rows outside each campaign's own run")
	fs.Parse(reorderFlags(args))

	if *horizon < 1 {
		return fmt.Errorf("horizon must be at least 1, got %d", *horizon)
	}

	files, err := pendingFiles(*dir)
	if err != nil {
		return err
	}
	fmt.Printf("%d file(s) to import from %s/\n\n", len(files), *dir)

	// The first file of a run empties the database; the rest of the batch adds to
	// it. Dropping several exports in the folder at once is one import of one
	// account, not several accounts in sequence.
	fresh := true
	for _, path := range files {
		if err := importOne(path, *dir, *dbPath, *horizon, *history, *skipFinetune, fresh, *fillAbsent); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		fresh = false
	}
	return nil
}

// pendingFiles lists the CSVs waiting in the data folder, creating it with a
// note inside if it does not exist yet. An empty folder is an error rather than
// a silent success: the user ran this to import something.
func pendingFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		for _, sub := range []string{importedName, reportsName} {
			if mkErr := os.MkdirAll(filepath.Join(dir, sub), 0o755); mkErr != nil {
				return nil, fmt.Errorf("creating %s: %w", dir, mkErr)
			}
		}
		_ = os.WriteFile(filepath.Join(dir, "README.txt"), []byte(dataFolderNote), 0o644)
		return nil, fmt.Errorf("created %s/ for you, and it is empty.\n"+
			"Put an exported CSV in there and run this again", dir)
	case !info.IsDir():
		return nil, fmt.Errorf("%s exists but is not a folder", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".csv") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no CSV files in %s/.\n"+
			"Export your campaign report, put the file there, and run this again.\n"+
			"Files already read are kept in %s/%s/", dir, dir, importedName)
	}
	return files, nil
}

const dataFolderNote = `Put your exported CSV files in this folder, then run:

    ./predictmarketing import

Each file is forecast with every model and then moved into imported/, so this
folder only ever holds what has not been read yet. The reports land in reports/.

At least 90 days of history is required. A year is better, two years is best.
`

// importOne is the whole job for a single file.
func importOne(path, dir, dbPath string, horizon, history int, skipFinetune, fresh, fillAbsent bool) error {
	// Before anything is wiped or any model is run. A reports folder that cannot be
	// written -- a synced folder gone read-only is the everyday version -- used to
	// surface only after clearDatabase had deleted the previous account's runs and
	// both models had been paid for, leaving a state the recovery skill does not
	// describe: new runs stored, no report, and last week's forecasts gone.
	if err := checkWritable(filepath.Join(dir, reportsName)); err != nil {
		return err
	}
	data, err := readCSVFilling(path, nil, "", fillAbsent)
	if err != nil {
		// A file below the model floor is also below the import's own, higher
		// gate. Report the gate the reader has to clear, not the one they hit
		// first, or they re-export to 32 days and are refused again.
		var short tooShort
		if errors.As(err, &short) {
			return enoughHistory(short.Days)
		}
		return err
	}
	if err := enoughHistory(len(data.Days)); err != nil {
		return err
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	fmt.Printf("%s: %d days, %s\n", filepath.Base(path), len(data.Days), historyVerdict(len(data.Days)))
	if data.RowsPerDay > 1 {
		fmt.Printf("  %d rows per day, split by %q\n", data.RowsPerDay, data.GroupBy)
	}
	fmt.Printf("  forecasting: %s\n", withConcepts(data, data.Names))
	if data.Currency != "" {
		fmt.Printf("  currency: %s\n", data.Currency)
	}
	for _, line := range blendLines(data, data.Names) {
		fmt.Println(line)
	}
	if len(data.Inactive) > 0 {
		fmt.Printf("  note: the (account) series includes those campaigns' history, so " +
			"its forecast assumes they keep spending. Per-campaign figures do not.\n")
	}
	if len(data.Renamed) > 0 {
		fmt.Printf("  renamed during this period, kept as one series: %s\n",
			strings.Join(data.Renamed, ", "))
	}
	if len(data.NotMetrics) > 0 {
		fmt.Printf("  numeric, but not a metric this forecasts: %s\n",
			strings.Join(data.NotMetrics, ", "))
	}
	fmt.Printf("  for %d: %s\n", len(data.Entities), strings.Join(data.Entities, ", "))
	for _, line := range exclusionLines(data) {
		fmt.Println(line)
	}

	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// Empty the database before storing anything -- but only now, after the file
	// has parsed. Wiping first would mean a malformed export destroyed the old
	// data and gave nothing back for it.
	days, err := nextDays(data.Days[len(data.Days)-1], horizon)
	if err != nil {
		return err
	}

	// Report 1: the two pretrained models, over each window that the file is long
	// enough to fill. They need no training and are ready in seconds.
	var runs []forecastRun
	byWindow := map[string][]forecastRun{}
	for _, w := range importWindows {
		if w.days > len(data.Days) {
			fmt.Printf("\n  skipping the %s window: the file has %d days\n", w.label, len(data.Days))
			continue
		}
		// A window the same length as the file is the file. Running it again
		// would store a second identical run and draw a second identical line.
		if w.days > 0 && w.days == len(data.Days) {
			fmt.Printf("\n  skipping the %s window: it is the whole file\n", w.label)
			continue
		}
		slice := lastDays(data, w.days)
		fmt.Printf("\n  %s window (%d days)\n", w.label, len(slice.Days))
		for _, model := range []string{"chronos2", "timesfm3"} {
			fmt.Printf("    running %s\n", model)
			r, err := forecastModel(model, runLabel(model, w.label), name, slice, days, horizon)
			if err != nil {
				return err
			}
			runs = append(runs, r)
			byWindow[w.label] = append(byWindow[w.label], r)
		}
	}

	// The average is the last 90 days, whatever the file's length -- measured
	// over 31 walk-forward origins, a 90-day window beat both the whole file and
	// 270 days at every horizon and at both account and campaign level.
	//
	// On a file of exactly 90 days the 90d window was skipped above as a
	// duplicate of the whole file, so `full` *is* the last 90 days there.
	source := byWindow[averageWindow]
	if len(source) == 0 {
		source = byWindow["full"]
	}
	avg, ok, err := averageRun(name, source, days, horizon)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("could not average the %d-day forecasts, so there is "+
			"nothing to draw", importMinDays)
	}
	fmt.Printf("\n  %s: the mean of %s\n", averageLabel, strings.Join(labelsOf(source), " and "))
	runs = append(runs, avg)

	// Everything answered. Only now is anything destroyed or written: the wipe,
	// the history, and every run in one go. Up to here a failure has cost the
	// user nothing but time.
	if fresh {
		before, err := storedRuns(db)
		if err != nil {
			return err
		}
		if err := clearDatabase(db); err != nil {
			return fmt.Errorf("clearing %s: %w", dbPath, err)
		}
		if before > 0 {
			fmt.Printf("  cleared %d earlier run(s) from %s -- an import starts a fresh account\n",
				before, filepath.Base(dbPath))
			for _, f := range orphanedReports(dir, name) {
				fmt.Printf("  superseded, and no longer backed by the database: %s\n", f)
			}
		}
	}
	if err := saveData(db, name, data); err != nil {
		return fmt.Errorf("writing to %s: %w", dbPath, err)
	}
	if err := saveRaw(db, name, data.Raw); err != nil {
		return fmt.Errorf("writing raw rows to %s: %w", dbPath, err)
	}
	for _, r := range runs {
		if err := storeRun(db, r, days); err != nil {
			return fmt.Errorf("storing %s: %w", r.Run.Model, err)
		}
	}

	// Every window is kept in the database so `accuracy` can score them, but only
	// the average is drawn: the individual model lines answer a question the
	// report is not asking, and six of them crowd out the one line that is
	// actually the recommendation.
	drawn := []forecastRun{avg}

	first := reportPath(dir, path, "models")
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(first), err)
	}
	if err := writeComparison(first, drawn, data, days, history); err != nil {
		return err
	}
	fmt.Printf("\n  report 1 of 2: %s\n", first)

	// The file has been read and stored, so it moves out of the way now rather
	// than after the fine-tune -- if training fails, the import still happened.
	moved, err := fileAway(path, dir)
	if err != nil {
		return err
	}
	fmt.Printf("  filed away:    %s\n", moved)

	if skipFinetune {
		fmt.Printf("\n  skipping the fine-tuned model (-no-finetune)\n")
		return nil
	}

	fmt.Printf("\n  training %s on this file, which takes a few minutes.\n", "chronos2ft")
	fmt.Printf("  Report 1 is already written -- open it while this runs.\n")
	if err := trainFinetune(moved, data.Names, data.GroupBy, data.LabelBy); err != nil {
		fmt.Printf("\n  the fine-tune did not finish: %v\n", err)
		fmt.Printf("  report 1 is unaffected. Train it later with:\n")
		fmt.Printf("    models/.venv/bin/python models/finetune.py %q \\\n", moved)
		fmt.Printf("        --metrics %q --group %q --label %q\n",
			strings.Join(data.Names, ","), data.GroupBy, data.LabelBy)
		return nil
	}

	// The fine-tune is given the whole file, not the 90-day window: it is the one
	// model that learns from the data rather than reading it, and more of it is
	// what training has to work with. `data` here is the full read.
	ft, err := forecastModel("chronos2ft", runLabel("chronos2ft", "full"), name, data, days, horizon)
	if err != nil {
		fmt.Printf("\n  the fine-tuned model would not run: %v\n", err)
		return nil
	}
	if err := storeRun(db, ft, days); err != nil {
		return err
	}
	runs = append(runs, ft)
	drawn = append(drawn, ft)

	second := reportPath(dir, path, "with-finetune")
	if err := writeComparison(second, drawn, data, days, history); err != nil {
		return err
	}
	fmt.Printf("\n  report 2 of 2: %s\n", second)
	fmt.Printf("  %s was trained on this data, so judge it on days after %s.\n",
		"chronos2ft", trainedThrough(ft.Run.ModelInfo))
	return nil
}

// storedRuns counts what a wipe is about to discard, so the run can say so.
func storedRuns(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n)
	return n, err
}

// enoughHistory refuses a file too short to forecast from, and says what would
// be better rather than only what is wrong.
func enoughHistory(n int) error {
	if n >= importMinDays {
		return nil
	}
	return fmt.Errorf("only %d days of history, and at least %d are needed.\n"+
		"  %d days  the minimum: enough for weekly shape\n"+
		"  %d days  better: a year, so annual seasonality can be learned\n"+
		"  %d days  best: two years, so that seasonality can be confirmed\n"+
		"Export a longer date range and try again",
		n, importMinDays, importMinDays, importGoodDays, importBestDays)
}

func historyVerdict(n int) string {
	switch {
	case n >= importBestDays:
		return "two years or more, which is as good as this gets"
	case n >= importGoodDays:
		return "over a year, so annual seasonality is learnable"
	default:
		return fmt.Sprintf("enough to forecast; %d days would be better", importGoodDays)
	}
}

// runModel forecasts every entity with one model and stores the run.
// forecastModel asks one model for one window and returns the answer. It writes
// nothing.
//
// The split from storeRun is what makes `import` safe to fail. The import
// empties the database before it stores anything, so if a model refused after
// that wipe the user was left with their old forecasts gone, a partial set of
// new ones and no report -- measured, worse than before they ran the command.
// Every model is asked first, and only once all of them have answered is
// anything destroyed or written.
func forecastModel(model, label, series string, data *Data, days []string,
	horizon int) (forecastRun, error) {

	w, err := startWorker(model)
	if err != nil {
		return forecastRun{}, fmt.Errorf("%s: %w", label, err)
	}
	defer w.Close()

	forecasts := map[string][][][]float64{}
	for _, entity := range data.Entities {
		rows := make([][]float64, len(data.Names))
		for i, n := range data.Names {
			rows[i] = data.Values[entity][n]
		}
		q, err := w.Forecast(rows, data.Names, horizon, w.Shake.Quantiles,
			data.Values[entity], nil)
		if err != nil {
			// The label, not the bare model name: the same model runs over
			// three windows, and "timesfm3 refused" does not say which.
			return forecastRun{}, fmt.Errorf("%s, %s: %w", label, entity, err)
		}
		forecasts[entity] = q
	}

	run := Run{
		ID: newRunID(), SeriesID: series, Model: label, Horizon: horizon,
		Metrics: data.Names, Entities: data.Entities, GroupBy: data.GroupBy,
		AsOf: data.Days[len(data.Days)-1], CreatedAt: time.Now(),
		InputHash: hashInput(data.Days, data.Entities, data.Names, data.Values),
		ModelInfo: w.Raw,
	}
	return forecastRun{Run: run, Shake: w.Shake, Values: forecasts}, nil
}

// storeRun writes a forecast that has already been made.
func storeRun(db *sql.DB, r forecastRun, days []string) error {
	return saveRun(db, r.Run, days, r.Shake.Quantiles, r.Values)
}

// lastDays returns the same dataset trimmed to its final n days.
//
// Only the numbers are trimmed. Names, Entities, GroupBy and the exclusion lists
// stay as the whole file decided them, so every window forecasts exactly the same
// campaigns and metrics -- which is what lets one chart carry all of them, since
// the report intersects entities across runs and would otherwise quietly drop any
// campaign a shorter window happened to classify differently.
//
// n <= 0, or n at or beyond the length of the file, returns the data unchanged.
func lastDays(d *Data, n int) *Data {
	if n <= 0 || n >= len(d.Days) {
		return d
	}
	cut := len(d.Days) - n
	out := *d
	out.Days = d.Days[cut:]
	out.Values = make(map[string]map[string][]float64, len(d.Values))
	for entity, metrics := range d.Values {
		m := make(map[string][]float64, len(metrics))
		for name, col := range metrics {
			m[name] = col[cut:]
		}
		out.Values[entity] = m
	}
	return &out
}

// averageRun stores the mean of several runs as a run in its own right.
//
// Averaged per entity, metric, day and quantile, so the interval is averaged as
// well as the median. Averaging monotonic quantiles keeps them monotonic, so the
// result needs no repair.
//
// Runs that declared a different quantile grid are left out rather than lined up
// by position: averaging a q0.1 with a q0.05 would produce a number that belongs
// to neither. If fewer than two runs remain there is nothing to average and the
// caller gets no run back.
func averageRun(series string, runs []forecastRun, days []string,
	horizon int) (forecastRun, bool, error) {

	if len(runs) < 2 {
		return forecastRun{}, false, nil
	}
	grid := runs[0].Shake.Quantiles
	same := func(q []float64) bool {
		if len(q) != len(grid) {
			return false
		}
		for i := range q {
			if q[i] != grid[i] {
				return false
			}
		}
		return true
	}
	var use []forecastRun
	for _, r := range runs {
		if same(r.Shake.Quantiles) {
			use = append(use, r)
		}
	}
	if len(use) < 2 {
		return forecastRun{}, false, nil
	}

	first := use[0]
	mean := map[string][][][]float64{}
	for _, entity := range first.Run.Entities {
		per := make([][][]float64, len(first.Run.Metrics))
		for mi := range first.Run.Metrics {
			per[mi] = make([][]float64, len(days))
			for di := range days {
				row := make([]float64, len(grid))
				for qi := range grid {
					sum := 0.0
					for _, r := range use {
						sum += r.Values[entity][mi][di][qi]
					}
					row[qi] = sum / float64(len(use))
				}
				per[mi][di] = row
			}
		}
		mean[entity] = per
	}

	info, err := json.Marshal(map[string]any{
		"model":     averageLabel,
		"averaged":  labelsOf(use),
		"quantiles": grid,
	})
	if err != nil {
		return forecastRun{}, false, err
	}
	run := Run{
		ID: newRunID(), SeriesID: series, Model: averageLabel, Horizon: horizon,
		Metrics: first.Run.Metrics, Entities: first.Run.Entities,
		GroupBy: first.Run.GroupBy, AsOf: first.Run.AsOf, CreatedAt: time.Now(),
		InputHash: first.Run.InputHash, ModelInfo: info,
	}
	return forecastRun{Run: run, Shake: Handshake{Quantiles: grid}, Values: mean}, true, nil
}

func labelsOf(runs []forecastRun) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.Run.Model
	}
	return out
}

// trainFinetune runs the trainer the same way the documentation tells you to.
//
// The metric and group columns come from the file that was just read, never from
// the trainer's own defaults. Those defaults are Google-Ads-shaped ("Cost",
// "Impr.", "Clicks", "Campaign"), and on any other export they either stop the
// trainer with "columns not in <file>" -- which is how this was found -- or, with
// no matching group column, silently train on one collapsed series.
func trainFinetune(csv string, metrics []string, groupBy, labelBy string) error {
	root := installDir()
	if root == "" {
		return fmt.Errorf("cannot find the models folder")
	}
	python := venvPython(root)
	if _, err := os.Stat(python); err != nil {
		return fmt.Errorf("no Python environment at %s -- run ./install.sh first", python)
	}
	abs, err := filepath.Abs(csv)
	if err != nil {
		return err
	}
	// No time limit. Training runs the steps it was given; a wall clock that cut
	// it short would leave an adapter that is undertrained but indistinguishable
	// from a finished one in every report that used it.
	args := []string{filepath.Join(root, "models", "finetune.py"), abs, "--steps", "2000"}
	if len(metrics) > 0 {
		args = append(args, "--metrics", strings.Join(metrics, ","))
	}
	if groupBy != "" {
		args = append(args, "--group", groupBy)
	}
	if labelBy != "" {
		args = append(args, "--label", labelBy)
	}
	cmd := exec.Command(python, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	return cmd.Run()
}

// checkWritable proves a directory can be created and written in, before the work
// that depends on it starts.
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	probe := filepath.Join(dir, ".pm-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write into %s -- the reports go there, so nothing "+
			"is started until it is writable: %w", dir, err)
	}
	f.Close()
	os.Remove(probe)
	return nil
}

// orphanedReports names report files left over from a previous import, whose runs
// the wipe has just deleted.
//
// They are not touched -- deleting someone's file is not this command's business --
// but they are named, because nothing else distinguishes them. Two imports leave
// two same-shaped files of near-identical size side by side in data/reports/, and
// opening the wrong one means sending a client a forecast whose underlying data no
// longer exists and which `report` will refuse to redraw. The only tell before
// this was a timestamp in the footer below the chart.
func orphanedReports(dir, keep string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, reportsName))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".html") || strings.HasPrefix(n, keep+"_") {
			continue
		}
		out = append(out, filepath.Join(reportsName, n))
	}
	sort.Strings(out)
	return out
}

// fileAway moves a CSV into data/imported/, never overwriting an earlier import
// of the same name.
func fileAway(path, dir string) (string, error) {
	dest := filepath.Join(dir, importedName)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(path)
	target := filepath.Join(dest, base)
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(base)
		target = filepath.Join(dest, fmt.Sprintf("%s-%s%s",
			strings.TrimSuffix(base, ext), time.Now().Format("2006-01-02-150405"), ext))
	}
	// The database and the reports are deliberately 0600, but a rename carries the
	// download's own mode, so the one file holding the client's raw numbers was
	// arriving 0644 while everything derived from it was protected.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(path, target); err != nil {
		return "", fmt.Errorf("moving %s into %s: %w", base, dest, err)
	}
	return target, nil
}

// reportPath names a report in data/reports/, keeping the name of the export it
// came from. Reports used to sit beside the CSV, which meant data/ filled up
// with a mixture of things waiting to be read and things already produced --
// and the folder is meant to say, at a glance, what has not been imported yet.
func reportPath(dir, csv, suffix string) string {
	base := strings.TrimSuffix(filepath.Base(csv), filepath.Ext(csv))
	return filepath.Join(dir, reportsName, base+"_"+suffix+".html")
}

func newRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// defaultPath anchors a default file or folder to the installation rather than
// to wherever the shell happens to be.
//
// models/ has always been found relative to the binary, but data/ and pm.db
// were plain relative paths, so running the program from another directory
// silently made a second, empty data/ there and a second database beside it --
// while still loading the models correctly, which is what made it look like it
// had worked. Forecasts scattered across several databases cannot be scored
// against later actuals, and `accuracy` would quietly have less history than
// the user thinks.
//
// An explicit -db or -data still wins; this only decides where "the default"
// points when nobody said.
func defaultPath(name string) string {
	if root := installDir(); root != "" && root != "." {
		return filepath.Join(root, name)
	}
	return name
}
