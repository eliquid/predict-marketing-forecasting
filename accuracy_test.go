package main

// Forecasts are kept so they can be checked against what actually happened.
//
// The comparison is a view, not a table: every number already exists in
// forecasts and series, and a third copy could only drift from them.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seed writes one run's forecast, and actuals for only the first two days, so
// the "not yet happened" case is covered too.
func seed(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}

	// three days of history, then a 3-day forecast from 2026-01-03
	d := &Data{
		Days:     []string{"2026-01-01", "2026-01-02", "2026-01-03"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity},
		Values: map[string]map[string][]float64{
			AccountEntity: {"Cost": {100, 110, 120}},
		},
	}
	if err := saveData(db, "s", d); err != nil {
		t.Fatal(err)
	}

	run := Run{ID: "run1", SeriesID: "s", Model: "chronos2", Horizon: 3,
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity},
		AsOf: "2026-01-03", CreatedAt: time.Now(), ModelInfo: []byte(`{}`)}
	// q10 / q50 / q90 for each of the three days
	vals := map[string][][][]float64{AccountEntity: {{
		{90, 100, 110},
		{180, 200, 220},
		{270, 300, 330},
	}}}
	days := []string{"2026-01-04", "2026-01-05", "2026-01-06"}
	if err := saveRun(db, run, days, []float64{0.1, 0.5, 0.9}, vals); err != nil {
		t.Fatal(err)
	}

	// the actuals that arrived later, for the first two days only
	later := &Data{
		Days:     []string{"2026-01-04", "2026-01-05"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity},
		Values: map[string]map[string][]float64{
			AccountEntity: {"Cost": {80, 200}}, // day 1 over-forecast, day 2 exact
		},
	}
	if err := saveData(db, "s", later); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAccuracyViewJoinsForecastToActual(t *testing.T) {
	db := seed(t, filepath.Join(t.TempDir(), "a.db"))
	defer db.Close()

	type row struct {
		day      string
		ahead    int
		forecast float64
		actual   sql.NullFloat64
		pct      sql.NullFloat64
		inside   sql.NullBool
	}
	rows, err := db.Query(`SELECT day, days_ahead, forecast, actual, pct_error, inside_range
	                       FROM forecast_accuracy ORDER BY day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.day, &r.ahead, &r.forecast, &r.actual, &r.pct, &r.inside); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (one per forecast day)", len(got))
	}

	// day 1: forecast 100, actual 80 -> 25% high, and 80 is outside 90..110
	if got[0].ahead != 1 {
		t.Errorf("days_ahead = %d, want 1", got[0].ahead)
	}
	if !got[0].actual.Valid || got[0].actual.Float64 != 80 {
		t.Errorf("actual = %v, want 80", got[0].actual)
	}
	if !got[0].pct.Valid || got[0].pct.Float64 < 24.9 || got[0].pct.Float64 > 25.1 {
		t.Errorf("pct_error = %v, want about +25", got[0].pct)
	}
	if !got[0].inside.Valid || got[0].inside.Bool {
		t.Error("80 is outside the 90-110 band and should not count as in range")
	}

	// day 2: forecast 200, actual 200 -> no error, inside the band
	if got[1].pct.Float64 != 0 {
		t.Errorf("pct_error = %v, want 0", got[1].pct.Float64)
	}
	if !got[1].inside.Bool {
		t.Error("200 is inside 180-220 and should count as in range")
	}

	// day 3 has not happened yet
	if got[2].actual.Valid {
		t.Errorf("day 3 has no actual yet, got %v", got[2].actual)
	}
	if got[2].ahead != 3 {
		t.Errorf("days_ahead = %d, want 3", got[2].ahead)
	}
}

// as_of is the last day of real data, which is not always the day you ran it.
func TestRunRecordsTheDataItWasBasedOn(t *testing.T) {
	db := seed(t, filepath.Join(t.TempDir(), "a.db"))
	defer db.Close()

	var asOf string
	if err := db.QueryRow(`SELECT as_of FROM runs WHERE id='run1'`).Scan(&asOf); err != nil {
		t.Fatal(err)
	}
	if asOf != "2026-01-03" {
		t.Errorf("as_of = %q, want the last day of input data", asOf)
	}
}

// Importing the same data again must not multiply the comparison rows.
func TestReimportingDoesNotDuplicateComparisons(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	db := seed(t, path)
	defer db.Close()

	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM forecast_accuracy`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	again := &Data{
		Days:     []string{"2026-01-04", "2026-01-05"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity},
		Values:   map[string]map[string][]float64{AccountEntity: {"Cost": {80, 200}}},
	}
	if err := saveData(db, "s", again); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before {
		t.Errorf("comparison rows went from %d to %d on re-import", before, after)
	}
}

// A name that matches nothing must say so. An empty table reads like "no data
// yet", which sends you looking in the wrong place.
func TestAccuracyRejectsUnknownFilters(t *testing.T) {
	db := seed(t, filepath.Join(t.TempDir(), "a.db"))
	defer db.Close()

	if err := known(db, "entity", "campaigns", "No Such Campaign", ""); err == nil {
		t.Error("an unknown campaign must be an error")
	} else if !strings.Contains(err.Error(), AccountEntity) {
		t.Errorf("the error should list the real campaigns, got: %v", err)
	}
	if err := known(db, "metric", "metrics", "Nonsense", ""); err == nil {
		t.Error("an unknown metric must be an error")
	}
	// the real ones pass
	if err := known(db, "entity", "campaigns", AccountEntity, ""); err != nil {
		t.Errorf("the account is real: %v", err)
	}
	if err := known(db, "metric", "metrics", "Cost", ""); err != nil {
		t.Errorf("Cost is real: %v", err)
	}
}

func TestAccuracyOnAnEmptyDatabaseSaysSo(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = known(db, "entity", "campaigns", AccountEntity, "")
	if err == nil || !strings.Contains(err.Error(), "no forecasts stored yet") {
		t.Errorf("an empty database should say so, got: %v", err)
	}
}

// A fine-tuned model has already seen the days it was trained on. Scoring it
// against them measures memorisation and flatters it enormously -- measured on
// real data, 9-14% MAPE on memorised days against 10-19% honest for the stock
// model. Those rows must be excluded, and the exclusion must be visible.
func TestTrainedOnDaysAreFlagged(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := &Data{
		Days:     []string{"2026-01-01", "2026-01-02", "2026-01-03"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity},
		Values:   map[string]map[string][]float64{AccountEntity: {"Cost": {100, 110, 120}}},
	}
	if err := saveData(db, "s", d); err != nil {
		t.Fatal(err)
	}
	// actuals for the days the forecast covers
	if err := saveData(db, "s", &Data{
		Days:     []string{"2026-01-04", "2026-01-05"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity},
		Values:   map[string]map[string][]float64{AccountEntity: {"Cost": {130, 140}}},
	}); err != nil {
		t.Fatal(err)
	}

	vals := map[string][][][]float64{AccountEntity: {{{120, 130, 140}, {130, 140, 150}}}}
	days := []string{"2026-01-04", "2026-01-05"}

	// a stock model: nothing is excluded
	plain := Run{ID: "plain", SeriesID: "s", Model: "chronos2", Horizon: 2,
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity},
		AsOf: "2026-01-03", CreatedAt: time.Now(), ModelInfo: []byte(`{"model":"chronos2"}`)}
	if err := saveRun(db, plain, days, []float64{0.1, 0.5, 0.9}, vals); err != nil {
		t.Fatal(err)
	}

	// a fine-tuned model trained through 2026-01-04: that day must be excluded
	ft := Run{ID: "ft", SeriesID: "s", Model: "chronos2ft", Horizon: 2,
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity},
		AsOf: "2026-01-03", CreatedAt: time.Now(),
		ModelInfo: []byte(`{"model":"chronos2ft","trained_through":"2026-01-04"}`)}
	if err := saveRun(db, ft, days, []float64{0.1, 0.5, 0.9}, vals); err != nil {
		t.Fatal(err)
	}

	count := func(model string, trainedOn int) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM forecast_accuracy
		                       WHERE model=? AND trained_on=?`, model, trainedOn).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count("chronos2", 1); got != 0 {
		t.Errorf("a model that was not fine-tuned must never be flagged, got %d rows", got)
	}
	if got := count("chronos2", 0); got != 2 {
		t.Errorf("stock model should have 2 scoreable rows, got %d", got)
	}
	if got := count("chronos2ft", 1); got != 1 {
		t.Errorf("the day inside the training window must be flagged, got %d rows", got)
	}
	if got := count("chronos2ft", 0); got != 1 {
		t.Errorf("the day after the training window must stay scoreable, got %d rows", got)
	}
}
