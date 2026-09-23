package main

// Rebuilding the reports from what is already stored.
//
// A report is a rendering of numbers the database already holds, so changing how
// a chart is drawn should not mean forecasting again -- and it certainly should
// not mean retraining, which is measured in minutes and gives different numbers.
// `report` re-renders the stored runs with the current code.
//
// This is deliberately not `import`: nothing is read, nothing is forecast,
// nothing is written to the database. It only reads.

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dbPath := fs.String("db", defaultPath("pm.db"), "database file")
	dir := fs.String("data", defaultPath(importDir), "folder whose reports/ to write into")
	series := fs.String("series", "", "which dataset (default: every one with forecasts)")
	history := fs.Int("history", 0, "days of past data drawn on the charts (0 = all of it)")
	fs.Parse(reorderFlags(args))

	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	names, err := storedSeries(db, *series)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no stored forecasts in %s. Run `predictmarketing import` first", *dbPath)
	}

	for _, name := range names {
		if err := rerenderSeries(db, name, *dir, *history); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// storedSeries lists the datasets that have forecasts, or checks the one asked for.
func storedSeries(db *sql.DB, want string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT series_id FROM runs ORDER BY series_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if want == "" {
		return all, nil
	}
	for _, s := range all {
		if strings.EqualFold(s, want) {
			return []string{s}, nil
		}
	}
	return nil, fmt.Errorf("no forecasts stored for %q (have: %s)", want, strings.Join(all, ", "))
}

// rerenderSeries rebuilds both reports for one dataset from its newest run of
// each model.
//
// Only runs sharing the newest as_of are used. Two runs made from different
// amounts of history are not comparable, and putting them on the same axes would
// invent a disagreement that is really a difference in what they were shown.
func rerenderSeries(db *sql.DB, name, dir string, history int) error {
	runs, err := latestRuns(db, name)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("no runs stored")
	}

	data, days, err := rebuildData(db, name, runs)
	if err != nil {
		return err
	}

	var pretrained, all []forecastRun
	for _, r := range runs {
		all = append(all, r)
		if trainedThrough(r.Run.ModelInfo) == "" {
			pretrained = append(pretrained, r)
		}
	}

	reports := []struct {
		suffix string
		runs   []forecastRun
	}{{"models", pretrained}}
	if len(all) > len(pretrained) {
		reports = append(reports, struct {
			suffix string
			runs   []forecastRun
		}{"with-finetune", all})
	}

	for _, rep := range reports {
		if len(rep.runs) == 0 {
			continue
		}
		path := reportPath(dir, name+".csv", rep.suffix)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeComparison(path, rep.runs, data, days, history); err != nil {
			return err
		}
		models := make([]string, len(rep.runs))
		for i, r := range rep.runs {
			models[i] = r.Run.Model
		}
		fmt.Printf("  %s  (%s)\n", path, strings.Join(models, ", "))
	}
	return nil
}

// latestRuns returns the newest run of each model, restricted to the newest
// as_of so every line on a chart was given the same history.
func latestRuns(db *sql.DB, name string) ([]forecastRun, error) {
	var asOf string
	if err := db.QueryRow(`SELECT MAX(as_of) FROM runs WHERE series_id=?`, name).Scan(&asOf); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, model, horizon, metrics, entities, group_by, as_of, model_info
	                       FROM runs WHERE series_id=? AND as_of=?
	                       ORDER BY created_at`, name, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	newest := map[string]Run{}
	var order []string
	for rows.Next() {
		var r Run
		var metrics, entities, info string
		if err := rows.Scan(&r.ID, &r.Model, &r.Horizon, &metrics, &entities,
			&r.GroupBy, &r.AsOf, &info); err != nil {
			return nil, err
		}
		r.SeriesID = name
		r.Metrics = strings.Split(metrics, ",")
		r.Entities = strings.Split(entities, "\x1f")
		r.ModelInfo = json.RawMessage(info)
		if _, seen := newest[r.Model]; !seen {
			order = append(order, r.Model)
		}
		newest[r.Model] = r // later rows are newer, so the last one wins
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []forecastRun
	for _, model := range order {
		r := newest[model]
		values, err := loadForecast(db, r)
		if err != nil {
			return nil, err
		}
		var hs Handshake
		_ = json.Unmarshal(r.ModelInfo, &hs)
		out = append(out, forecastRun{Run: r, Shake: hs, Values: values})
	}
	return out, nil
}

// loadForecast reads one run's stored quantiles back into the shape the report
// expects: entity -> [metric][day][quantile].
func loadForecast(db *sql.DB, r Run) (map[string][][][]float64, error) {
	rows, err := db.Query(`SELECT entity, metric, day, quantile, value FROM forecasts
	                       WHERE run_id=? ORDER BY entity, metric, day, quantile`, r.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// entity -> metric -> day -> quantiles, before it is flattened into slices
	byDay := map[string]map[string]map[string][]float64{}
	dayOrder := map[string][]string{}
	for rows.Next() {
		var entity, metric, day string
		var q, v float64
		if err := rows.Scan(&entity, &metric, &day, &q, &v); err != nil {
			return nil, err
		}
		if byDay[entity] == nil {
			byDay[entity] = map[string]map[string][]float64{}
		}
		if byDay[entity][metric] == nil {
			byDay[entity][metric] = map[string][]float64{}
		}
		key := entity + "\x1f" + metric
		if _, seen := byDay[entity][metric][day]; !seen {
			dayOrder[key] = append(dayOrder[key], day)
		}
		byDay[entity][metric][day] = append(byDay[entity][metric][day], v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string][][][]float64{}
	for _, entity := range r.Entities {
		per := make([][][]float64, len(r.Metrics))
		for mi, metric := range r.Metrics {
			days := dayOrder[entity+"\x1f"+metric]
			sort.Strings(days)
			per[mi] = make([][]float64, len(days))
			for di, day := range days {
				per[mi][di] = byDay[entity][metric][day]
			}
		}
		out[entity] = per
	}
	return out, nil
}

// rebuildData reassembles just enough of a Data for the report: the history it
// draws, and which entities were stored but not forecast.
//
// Percentage formatting is not recoverable -- series stores the number, not that
// it was written with a % sign -- so a rate re-rendered this way loses its sign.
// Re-importing restores it.
func rebuildData(db *sql.DB, name string, runs []forecastRun) (*Data, []string, error) {
	first := runs[0].Run
	data := &Data{
		Names:    first.Metrics,
		Entities: first.Entities,
		GroupBy:  first.GroupBy,
		Percent:  map[string]bool{},
		Values:   map[string]map[string][]float64{},
	}

	var days []string
	for _, entity := range first.Entities {
		data.Values[entity] = map[string][]float64{}
		for _, metric := range first.Metrics {
			points, err := loadSeries(db, name, entity, metric)
			if err != nil {
				return nil, nil, err
			}
			col := make([]float64, len(points))
			for i, p := range points {
				col[i] = p.Value
			}
			data.Values[entity][metric] = col
			if len(points) > len(data.Days) {
				data.Days = make([]string, len(points))
				for i, p := range points {
					data.Days[i] = p.Day
				}
			}
		}
	}

	// Everything stored for this dataset that no run forecast was excluded for
	// having nothing to forecast; say so, as the original report did.
	rows, err := db.Query(`SELECT DISTINCT entity FROM series WHERE series_id=? ORDER BY entity`, name)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	forecast := map[string]bool{}
	for _, e := range first.Entities {
		forecast[e] = true
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, nil, err
		}
		if !forecast[e] {
			data.Inactive = append(data.Inactive, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// The forecast days come from the stored forecast itself.
	for _, v := range runs[0].Values {
		if len(v) > 0 {
			days = make([]string, len(v[0]))
			break
		}
	}
	stored, err := forecastDays(db, first.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(stored) > 0 {
		days = stored
	}
	if len(days) == 0 {
		return nil, nil, fmt.Errorf("the stored run has no forecast days")
	}
	return data, days, nil
}

func forecastDays(db *sql.DB, runID string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT day FROM forecasts WHERE run_id=? ORDER BY day`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
