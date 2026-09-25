package main

// Round 32: re-importing. A corrected export arrives with the same days and
// different numbers, and the recurring job imports it over the top of the old
// one. series updates in place and raw is replaced per source, so the stored
// numbers must end up equal to the newest file rather than a mixture.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reimportCSV(t *testing.T, dir string, camps []string, days int, base float64) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost\n")
	d := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < days; i++ {
		day := d.AddDate(0, 0, i).Format("2006-01-02")
		for j, c := range camps {
			fmt.Fprintf(&b, "%s,%s,%.2f\n", day, c, base+float64(i+j*10))
		}
	}
	p := filepath.Join(dir, "c.csv")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Re-importing a corrected export must leave the stored numbers equal to the new
// file, not a mixture of both imports.
func TestReimportCorrectedExport(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	n := smallestUsefulSeries + 4
	first, err := readCSV(reimportCSV(t, dir, []string{"A", "B"}, n, 100), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "s", first); err != nil {
		t.Fatal(err)
	}
	if err := saveRaw(db, "s", first.Raw); err != nil {
		t.Fatal(err)
	}

	// Same shape, different numbers: a corrected export.
	second, err := readCSV(reimportCSV(t, dir, []string{"A", "B"}, n, 500), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "s", second); err != nil {
		t.Fatal(err)
	}
	if err := saveRaw(db, "s", second.Raw); err != nil {
		t.Fatal(err)
	}

	for _, entity := range []string{AccountEntity, "A", "B"} {
		pts, err := loadSeries(db, "s", entity, "Cost")
		if err != nil {
			t.Fatal(err)
		}
		if len(pts) != n {
			t.Errorf("%s: %d days stored, want %d", entity, len(pts), n)
		}
		want := second.Values[entity]["Cost"]
		for i, p := range pts {
			if p.Value != want[i] {
				t.Errorf("%s day %d: stored %v, corrected file says %v",
					entity, i, p.Value, want[i])
				break
			}
		}
	}
	var rawRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM raw WHERE source='s'`).Scan(&rawRows); err != nil {
		t.Fatal(err)
	}
	if rawRows != len(second.Raw) {
		t.Errorf("raw holds %d rows, the corrected file has %d", rawRows, len(second.Raw))
	}
}

// A campaign dropped from a later export leaves its old rows in series. That is
// by design -- series is history, not a mirror of the newest file -- but the
// account total for those days must still be the new file's total, not a mix.
func TestReimportWithACampaignRemoved(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	n := smallestUsefulSeries + 4
	two, err := readCSV(reimportCSV(t, dir, []string{"A", "B"}, n, 100), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "s", two); err != nil {
		t.Fatal(err)
	}
	one, err := readCSV(reimportCSV(t, filepath.Join(dir), []string{"A"}, n, 100), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "s", one); err != nil {
		t.Fatal(err)
	}

	pts, err := loadSeries(db, "s", AccountEntity, "Cost")
	if err != nil {
		t.Fatal(err)
	}
	want := one.Values[AccountEntity]["Cost"]
	for i, p := range pts {
		if p.Value != want[i] {
			t.Fatalf("account day %d = %v, but the newest file totals %v (stale sum kept)",
				i, p.Value, want[i])
		}
	}
}

// Two datasets in one file must not see each other.
func TestSeriesAreIsolated(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	n := smallestUsefulSeries + 2
	a, err := readCSV(reimportCSV(t, dir, []string{"A"}, n, 100), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "alpha", a); err != nil {
		t.Fatal(err)
	}
	b, err := readCSV(reimportCSV(t, filepath.Join(dir), []string{"A"}, n, 900), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "beta", b); err != nil {
		t.Fatal(err)
	}

	// One campaign per day means no campaign split, so the account total is the
	// series. Isolation here is about series_id, not entity.
	pa, _ := loadSeries(db, "alpha", AccountEntity, "Cost")
	pb, _ := loadSeries(db, "beta", AccountEntity, "Cost")
	if len(pa) != n || len(pb) != n {
		t.Fatalf("alpha %d rows, beta %d rows, want %d each", len(pa), len(pb), n)
	}
	if pa[0].Value == pb[0].Value {
		t.Errorf("both series report %v; one overwrote the other", pa[0].Value)
	}
}

// Re-importing replaces the dataset rather than merging into it.
//
// saveData used to be an upsert with no delete, so it only touched the cells the
// new file covered. A narrower export -- fewer days, fewer campaigns, or an
// import for an entirely different account under the same name -- left
// everything it did not mention behind. `raw` was then exactly the newest file
// while `series` was the union of every import ever done, and since
// forecast_accuracy joins to `series`, those disowned rows kept being scored as
// though they were actuals.
func TestReimportReplacesTheDatasetRatherThanMerging(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := &Data{
		Days:     []string{"2026-01-01", "2026-01-02", "2026-01-03"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity, "Alpha", "Ghost"},
		Values: map[string]map[string][]float64{
			AccountEntity: {"Cost": {600, 600, 600}},
			"Alpha":       {"Cost": {100, 100, 100}},
			"Ghost":       {"Cost": {500, 500, 500}},
		},
	}
	if err := saveData(db, "acct", first); err != nil {
		t.Fatal(err)
	}

	// A different account entirely: shorter, one campaign, different numbers.
	second := &Data{
		Days:     []string{"2026-01-01", "2026-01-02"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity, "Beta"},
		Values: map[string]map[string][]float64{
			AccountEntity: {"Cost": {7, 8}},
			"Beta":        {"Cost": {7, 8}},
		},
	}
	if err := saveData(db, "acct", second); err != nil {
		t.Fatal(err)
	}

	var entities, days int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT entity), COUNT(DISTINCT day)
	                       FROM series WHERE series_id='acct'`).Scan(&entities, &days); err != nil {
		t.Fatal(err)
	}
	if entities != 2 || days != 2 {
		t.Errorf("after re-import: %d entities over %d days, want 2 and 2 — the "+
			"previous account's rows are still there", entities, days)
	}
	for _, gone := range []string{"Alpha", "Ghost"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM series WHERE series_id='acct' AND entity=?`,
			gone).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%q survived the re-import with %d rows", gone, n)
		}
	}
	pts, err := loadSeries(db, "acct", AccountEntity, "Cost")
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || pts[0].Value != 7 {
		t.Errorf("account series = %v, want the second import's numbers", pts)
	}

	// A different dataset in the same database must be untouched by any of this.
	if err := saveData(db, "other", first); err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "acct", second); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM series WHERE series_id='other'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Errorf("re-importing 'acct' changed 'other': %d rows, want 9", n)
	}
}

// An import is someone bringing in an account, so it empties the database
// first: the numbers already there belong to whatever was imported before, and
// merging two accounts under one roof produces totals that describe nothing.
//
// This deliberately discards the forecast record as well. `runs` and
// `forecasts` are what `accuracy` scores once the actuals arrive, and after a
// wipe there is nothing left to score -- see AGENTS.md §4a for the trade.
func TestClearDatabaseEmptiesEveryTable(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := &Data{
		Days:     []string{"2026-01-01", "2026-01-02"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity},
		Values:   map[string]map[string][]float64{AccountEntity: {"Cost": {1, 2}}},
	}
	if err := saveData(db, "s", d); err != nil {
		t.Fatal(err)
	}
	if err := saveRaw(db, "s", []RawRow{{Line: 1, Day: "2026-01-01", Data: map[string]string{"x": "y"}}}); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: "r1", SeriesID: "s", Model: "chronos2@90d", Horizon: 1,
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity},
		AsOf: "2026-01-02", CreatedAt: time.Now(), InputHash: "h", ModelInfo: []byte(`{}`)}
	if err := saveRun(db, run, []string{"2026-01-03"}, []float64{0.1, 0.5, 0.9},
		map[string][][][]float64{AccountEntity: {{{1, 2, 3}}}}); err != nil {
		t.Fatal(err)
	}

	for _, tbl := range []string{"series", "raw", "runs", "forecasts"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("%s is empty before the wipe; the test proves nothing", tbl)
		}
	}

	if err := clearDatabase(db); err != nil {
		t.Fatal(err)
	}

	for _, tbl := range []string{"series", "raw", "runs", "forecasts"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d rows after the wipe", tbl, n)
		}
	}

	// The schema has to survive, or the next save fails on a missing table.
	if err := saveData(db, "s2", d); err != nil {
		t.Errorf("the database is unusable after a wipe: %v", err)
	}
	// And the derived objects too -- accuracy reads the view.
	var v int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forecast_accuracy`).Scan(&v); err != nil {
		t.Errorf("forecast_accuracy did not survive the wipe: %v", err)
	}
}
