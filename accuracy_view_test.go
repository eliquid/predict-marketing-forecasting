package main

// Round 31: the arithmetic inside forecast_accuracy, checked against values
// computed by hand. The view is where every reported accuracy number comes from,
// and it was rewritten for the forecasts_median index, so its output is worth
// pinning independently of how fast it runs.

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// The accuracy view does the arithmetic that every reported number comes from, so
// check it against values computed by hand rather than trusting the SQL.
func TestAccuracyViewArithmetic(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// actual 100, forecast median 110, band 90..130 -> error +10, 10% high, inside.
	if _, err := db.Exec(
		`INSERT INTO series VALUES ('s','(account)','Cost','2026-03-05',100)`); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "r1", SeriesID: "s", Model: "chronos2", Horizon: 1,
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity},
		AsOf: "2026-03-01", CreatedAt: time.Now(), InputHash: "h",
		ModelInfo: []byte(`{}`)}
	vals := map[string][][][]float64{AccountEntity: {{{90, 110, 130}}}}
	if err := saveRun(db, run, []string{"2026-03-05"}, []float64{0.1, 0.5, 0.9}, vals); err != nil {
		t.Fatal(err)
	}

	var ahead int
	var forecast, actual, errv, abserr, pct, low, high float64
	var inside, trainedOn int
	if err := db.QueryRow(`SELECT days_ahead, forecast, actual, error, abs_error,
	    pct_error, low, high, inside_range, trained_on
	    FROM forecast_accuracy WHERE entity='(account)' AND metric='Cost'`).
		Scan(&ahead, &forecast, &actual, &errv, &abserr, &pct, &low, &high,
			&inside, &trainedOn); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"days_ahead", float64(ahead), 4}, // 2026-03-01 -> 2026-03-05
		{"forecast (median)", forecast, 110},
		{"actual", actual, 100},
		{"error", errv, 10},
		{"abs_error", abserr, 10},
		{"pct_error", pct, 10},
		{"low (q10)", low, 90},
		{"high (q90)", high, 130},
		{"inside_range", float64(inside), 1},
		{"trained_on", float64(trainedOn), 0},
	} {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// An actual outside the q10..q90 band must read as outside, and a zero actual must
// leave pct_error NULL rather than dividing by zero.
func TestAccuracyViewEdges(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO series VALUES
	    ('s','(account)','Cost','2026-03-05',500),
	    ('s','(account)','Clicks','2026-03-05',0)`); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "r1", SeriesID: "s", Model: "chronos2", Horizon: 1,
		Metrics: []string{"Cost", "Clicks"}, Entities: []string{AccountEntity},
		AsOf: "2026-03-04", CreatedAt: time.Now(), InputHash: "h",
		ModelInfo: []byte(`{}`)}
	vals := map[string][][][]float64{
		AccountEntity: {{{90, 110, 130}}, {{1, 2, 3}}},
	}
	if err := saveRun(db, run, []string{"2026-03-05"}, []float64{0.1, 0.5, 0.9}, vals); err != nil {
		t.Fatal(err)
	}

	var inside int
	if err := db.QueryRow(`SELECT inside_range FROM forecast_accuracy
	    WHERE metric='Cost'`).Scan(&inside); err != nil {
		t.Fatal(err)
	}
	if inside != 0 {
		t.Errorf("an actual of 500 outside a 90..130 band reported inside_range=%d", inside)
	}
	var pct *float64
	if err := db.QueryRow(`SELECT pct_error FROM forecast_accuracy
	    WHERE metric='Clicks'`).Scan(&pct); err != nil {
		t.Fatal(err)
	}
	if pct != nil {
		t.Errorf("pct_error against an actual of zero = %v, want NULL", *pct)
	}
}

// The same numbers must hash the same, and different numbers must not.
func TestInputHashDeterministic(t *testing.T) {
	days := []string{"2026-03-01", "2026-03-02"}
	ents, mets := []string{AccountEntity}, []string{"Cost"}
	v1 := map[string]map[string][]float64{AccountEntity: {"Cost": {1, 2}}}
	v2 := map[string]map[string][]float64{AccountEntity: {"Cost": {1, 2.0000001}}}

	a := hashInput(days, ents, mets, v1)
	if b := hashInput(days, ents, mets, v1); a != b {
		t.Errorf("same input hashed differently:\n%s\n%s", a, b)
	}
	if c := hashInput(days, ents, mets, v2); a == c {
		t.Error("a changed value produced the same hash")
	}
}
