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

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dir := fs.String("data", defaultPath(importDir), "folder to read CSVs from")
	horizon := fs.Int("horizon", 7, "days ahead")
	history := fs.Int("history", 0, "days of past data drawn on the charts (0 = all of it)")
	dbPath := fs.String("db", defaultPath("pm.db"), "database file")
	skipFinetune := fs.Bool("no-finetune", false,
		"skip the fine-tuned model and write only the first report")
	fs.Parse(reorderFlags(args))

	if *horizon < 1 {
		return fmt.Errorf("horizon must be at least 1, got %d", *horizon)
	}

	files, err := pendingFiles(*dir)
	if err != nil {
		return err
	}
	fmt.Printf("%d file(s) to import from %s/\n\n", len(files), *dir)

	for _, path := range files {
		if err := importOne(path, *dir, *dbPath, *horizon, *history, *skipFinetune); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
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
func importOne(path, dir, dbPath string, horizon, history int, skipFinetune bool) error {
	data, err := readCSV(path, nil, "")
	if err != nil {
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
	fmt.Printf("  forecasting: %s\n", strings.Join(data.Names, ", "))
	fmt.Printf("  for %d: %s\n", len(data.Entities), strings.Join(data.Entities, ", "))
	for _, line := range exclusionLines(data) {
		fmt.Println(line)
	}

	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := saveData(db, name, data); err != nil {
		return fmt.Errorf("writing to %s: %w", dbPath, err)
	}
	if err := saveRaw(db, name, data.Raw); err != nil {
		return fmt.Errorf("writing raw rows to %s: %w", dbPath, err)
	}

	days, err := nextDays(data.Days[len(data.Days)-1], horizon)
	if err != nil {
		return err
	}

	// Report 1: the two pretrained models, which need no training and are ready
	// in seconds.
	var runs []forecastRun
	for _, model := range []string{"chronos2", "timesfm3"} {
		fmt.Printf("\n  running %s\n", model)
		r, err := runModel(db, model, name, data, days, horizon)
		if err != nil {
			return err
		}
		runs = append(runs, r)
	}

	first := reportPath(dir, path, "models")
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(first), err)
	}
	if err := writeComparison(first, runs, data, days, history); err != nil {
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
	if err := trainFinetune(moved); err != nil {
		fmt.Printf("\n  the fine-tune did not finish: %v\n", err)
		fmt.Printf("  report 1 is unaffected. Train it later with:\n")
		fmt.Printf("    models/.venv/bin/python models/finetune.py %q\n", moved)
		return nil
	}

	ft, err := runModel(db, "chronos2ft", name, data, days, horizon)
	if err != nil {
		fmt.Printf("\n  the fine-tuned model would not run: %v\n", err)
		return nil
	}
	runs = append(runs, ft)

	second := reportPath(dir, path, "with-finetune")
	if err := writeComparison(second, runs, data, days, history); err != nil {
		return err
	}
	fmt.Printf("\n  report 2 of 2: %s\n", second)
	fmt.Printf("  %s was trained on this data, so judge it on days after %s.\n",
		"chronos2ft", trainedThrough(ft.Run.ModelInfo))
	return nil
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
func runModel(db *sql.DB, model, series string, data *Data, days []string,
	horizon int) (forecastRun, error) {

	w, err := startWorker(model)
	if err != nil {
		return forecastRun{}, fmt.Errorf("%s: %w", model, err)
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
			return forecastRun{}, fmt.Errorf("%s, %s: %w", model, entity, err)
		}
		forecasts[entity] = q
	}

	run := Run{
		ID: newRunID(), SeriesID: series, Model: model, Horizon: horizon,
		Metrics: data.Names, Entities: data.Entities, GroupBy: data.GroupBy,
		AsOf: data.Days[len(data.Days)-1], CreatedAt: time.Now(),
		InputHash: hashInput(data.Days, data.Entities, data.Names, data.Values),
		ModelInfo: w.Raw,
	}
	if err := saveRun(db, run, days, w.Shake.Quantiles, forecasts); err != nil {
		return forecastRun{}, err
	}
	return forecastRun{Run: run, Shake: w.Shake, Values: forecasts}, nil
}

// trainFinetune runs the trainer the same way the documentation tells you to.
func trainFinetune(csv string) error {
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
	cmd := exec.Command(python, filepath.Join(root, "models", "finetune.py"), abs,
		"--steps", "2000")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	return cmd.Run()
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
