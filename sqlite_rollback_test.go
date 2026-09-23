package main

// A failed saveRun must leave nothing: the run row goes in before the forecast
// rows, so a partial commit would leave a run that explains no numbers.

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRollbackLeavesNothing(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "rb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// An entity present in Entities but absent from values makes saveRun fail
	// partway, after the run row and some forecasts are already inserted.
	r := Run{ID: "r1", SeriesID: "s", Model: "chronos2", Horizon: 1,
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity, "missing"},
		AsOf: "2026-01-01", CreatedAt: time.Now(), InputHash: "h",
		ModelInfo: []byte(`{}`)}
	vals := map[string][][][]float64{AccountEntity: {{{1, 2, 3}}}}
	if err := saveRun(db, r, []string{"2026-01-02"}, []float64{0.1, 0.5, 0.9}, vals); err == nil {
		t.Fatal("saveRun accepted an entity it had no forecast for")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM runs`, `SELECT COUNT(*) FROM forecasts`} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s left %d rows after a failed run", q, n)
		}
	}
}
