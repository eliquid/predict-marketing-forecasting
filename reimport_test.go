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
