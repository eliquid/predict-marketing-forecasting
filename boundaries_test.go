package main

// Round 30: calendar edges and numeric boundaries. The overflow test here found a
// real hole: parseCell keeps non-finite values out of the input, but summing
// campaigns into the account total is a separate step that could reach infinity
// on its own, and SQLite stores infinity without complaint.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.csv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Two campaigns each just under the float64 max sum to +Inf in the account total.
// parseCell rejects a non-finite *input*, but the aggregation is a separate step.
func TestAccountSumCannotOverflow(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost\n")
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < smallestUsefulSeries+4; i++ {
		d := day.AddDate(0, 0, i).Format("2006-01-02")
		fmt.Fprintf(&b, "%s,A,1e308\n%s,B,1e308\n", d, d)
	}
	d, err := readCSV(write(t, b.String()), nil, "")
	if err != nil {
		t.Logf("refused outright: %v", err)
		return
	}
	for m, col := range d.Values[AccountEntity] {
		for i, v := range col {
			if math.IsInf(v, 0) || math.IsNaN(v) {
				t.Fatalf("account total for %s day %d overflowed to %v", m, i, v)
			}
		}
	}
}

func TestCalendarEdges(t *testing.T) {
	cases := map[string]time.Time{
		"leap day":      time.Date(2028, 2, 20, 0, 0, 0, 0, time.UTC),
		"year boundary": time.Date(2025, 12, 20, 0, 0, 0, 0, time.UTC),
		"month lengths": time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC),
		"US DST spring": time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"US DST fall":   time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
	}
	for name, start := range cases {
		var b strings.Builder
		b.WriteString("date,spend\n")
		n := smallestUsefulSeries + 20
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "%s,%d\n", start.AddDate(0, 0, i).Format("2006-01-02"), 100+i)
		}
		d, err := readCSV(write(t, b.String()), nil, "")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(d.Days) != n {
			t.Errorf("%s: read %d days, wrote %d", name, len(d.Days), n)
		}
		for k := 1; k < len(d.Days); k++ {
			if d.Days[k-1] >= d.Days[k] {
				t.Errorf("%s: %s then %s", name, d.Days[k-1], d.Days[k])
			}
		}
	}
}

func TestBoundaryShapes(t *testing.T) {
	mk := func(n int, val string) string {
		var b strings.Builder
		b.WriteString("date,spend\n")
		day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "%s,%s\n", day.AddDate(0, 0, i).Format("2006-01-02"), val)
		}
		return b.String()
	}
	if _, err := readCSV(write(t, mk(smallestUsefulSeries, "100")), nil, ""); err != nil {
		t.Errorf("exactly the minimum length was refused: %v", err)
	}
	if _, err := readCSV(write(t, mk(smallestUsefulSeries-1, "100")), nil, ""); err == nil {
		t.Error("one row below the minimum was accepted")
	}
	// An all-zero series is legitimate (a paused campaign) and must not divide by zero.
	if _, err := readCSV(write(t, mk(smallestUsefulSeries+4, "0")), nil, ""); err != nil {
		t.Errorf("an all-zero series was refused: %v", err)
	}
	// Very long and unicode entity names.
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost\n")
	long := strings.Repeat("キャンペーン", 40)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < smallestUsefulSeries+4; i++ {
		d := day.AddDate(0, 0, i).Format("2006-01-02")
		fmt.Fprintf(&b, "%s,%s,%d\n%s,plain,%d\n", d, long, 10+i, d, 20+i)
	}
	d, err := readCSV(write(t, b.String()), nil, "")
	if err != nil {
		t.Fatalf("unicode campaign name refused: %v", err)
	}
	if len(d.Entities) != 3 {
		t.Errorf("got %d entities, want account + 2: %v", len(d.Entities), d.Entities)
	}
}

// Reports get emailed and shared, and campaign names come from a downloaded file,
// so a name is untrusted input rendered into HTML. html/template escapes by
// default; this pins that it is still true, including inside the SVG chart, where
// a name is drawn as text and a breakout would be a live script tag.
func TestReportEscapesHostileEntityNames(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`"><img src=x onerror=alert(2)>`,
		`</text></svg><script>alert(3)</script>`,
		`'; DROP TABLE forecasts; --`,
	}
	days := []string{"2026-03-01", "2026-03-02"}
	quantiles := []float64{0.1, 0.5, 0.9}

	data := &Data{
		Days:  []string{"2026-02-26", "2026-02-27", "2026-02-28"},
		Names: []string{"Cost"}, Entities: append([]string{AccountEntity}, payloads...),
		GroupBy: "Campaign", Percent: map[string]bool{},
		Values: map[string]map[string][]float64{},
	}
	values := map[string][][][]float64{}
	for _, e := range data.Entities {
		data.Values[e] = map[string][]float64{"Cost": {1, 2, 3}}
		values[e] = [][][]float64{{{1, 2, 3}, {2, 3, 4}}}
	}

	run := Run{ID: "r", SeriesID: "s", Model: "chronos2", Horizon: len(days),
		Metrics: []string{"Cost"}, Entities: data.Entities, AsOf: "2026-02-28",
		CreatedAt: time.Now(), InputHash: "h", ModelInfo: []byte(`{}`)}

	path := filepath.Join(t.TempDir(), "r.html")
	if err := writeReport(path, run, data, days, quantiles, values, 90); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)

	for _, p := range payloads {
		if strings.Contains(page, p) {
			t.Errorf("payload rendered unescaped: %q", p)
		}
	}
	// One script tag only: the htmx that template.go inlines on purpose.
	if n := strings.Count(page, "<script"); n != 1 {
		t.Errorf("%d script tags in the report, want 1 (the inlined htmx)", n)
	}
	if strings.Contains(page, `"><img`) {
		t.Error("a name broke out of an attribute")
	}
}

// A file that does not split into columns is nearly always one of two ordinary
// things: saved with a different separator, or a platform export that opens with
// a title and a date range above the header. "need at least 2 columns" alone
// sends people looking in the wrong place.
func TestOneColumnErrorNamesTheLikelyCause(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"Day;Cost;Clicks", "semicolon"},
		{"Day\tCost\tClicks", "tab"},
		{"Campaign report", "title or a date range"},
	} {
		if got := whyOneColumn(c.line); !strings.Contains(got, c.want) {
			t.Errorf("whyOneColumn(%q) = %q, want it to mention %q", c.line, got, c.want)
		}
	}
}

// A Latin-1 export parses happily but its column names arrive mangled, and that
// mangled name is what gets stored as the metric and printed in the report.
// Accented names in real UTF-8 must keep working.
func TestNonUTF8HeaderRefused(t *testing.T) {
	dir := t.TempDir()
	rows := func(c1, c2 string) []byte {
		var b strings.Builder
		fmt.Fprintf(&b, "Day,%s,%s\n", c1, c2)
		day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < smallestUsefulSeries+4; i++ {
			fmt.Fprintf(&b, "%s,%d,%d\n", day.AddDate(0, 0, i).Format("2006-01-02"), 100+i, 5+i)
		}
		return []byte(b.String())
	}

	bad := filepath.Join(dir, "latin1.csv")
	// "Coût" as Latin-1: the û is a lone 0xFB byte, which is not valid UTF-8.
	if err := os.WriteFile(bad, rows("Co\xfbt", "Clics"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCSV(bad, nil, ""); err == nil {
		t.Error("a Latin-1 header was accepted; its metric name would be stored mangled")
	} else if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("refused for the wrong reason: %v", err)
	}

	good := filepath.Join(dir, "utf8.csv")
	if err := os.WriteFile(good, rows("Coût", "Clics"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := readCSV(good, nil, "")
	if err != nil {
		t.Fatalf("a valid UTF-8 header with accents was refused: %v", err)
	}
	if len(d.Names) != 2 || d.Names[0] != "Coût" {
		t.Errorf("accented column name did not survive: %v", d.Names)
	}
}

// Column 1 has to be the date. When a file keeps its dates elsewhere, the failure
// used to surface as `unrecognised date "100"` on row 2, which names the symptom
// and not the cause.
func TestDateColumnElsewhereIsExplained(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("Cost,Clicks,Day\n")
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < smallestUsefulSeries+4; i++ {
		fmt.Fprintf(&b, "%d,%d,%s\n", 100+i, 5+i, day.AddDate(0, 0, i).Format("2006-01-02"))
	}
	p := filepath.Join(dir, "datelast.csv")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readCSV(p, nil, "")
	if err == nil {
		t.Fatal("a file with no date in column 1 was accepted")
	}
	for _, want := range []string{"Column 1 must hold the date", `column 3 ("Day")`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	// A file whose dates really are first must not pick up the hint.
	if got := whereTheDatesAre([]string{"Day", "Cost", "Clicks"}); got != "" {
		t.Errorf("hint fired on a correct file: %q", got)
	}
}

// A campaign exported once as "Brand" and once as "BRAND" is one campaign to
// everyone except a string comparison. Refusing is right -- silently merging
// could fold two real campaigns together -- but the refusal should say what is
// actually wrong instead of leaving the reader to count distinct values.
func TestNearDuplicateCampaignNamesExplained(t *testing.T) {
	got := nearDuplicates(map[string]bool{"Brand": true, "BRAND": true, "Shop": true}, 2)
	if !strings.Contains(got, "capitalisation") {
		t.Errorf("no hint for names differing only in case: %q", got)
	}
	// Three genuinely different campaigns against two rows per day is a real
	// mismatch with no case explanation, and must not be given a bogus one.
	if got := nearDuplicates(map[string]bool{"A": true, "B": true, "C": true}, 2); got != "" {
		t.Errorf("invented a capitalisation explanation for distinct names: %q", got)
	}
	// The normal case: counts already agree, nothing to say.
	if got := nearDuplicates(map[string]bool{"A": true, "B": true}, 2); got != "" {
		t.Errorf("commented on a file that is fine: %q", got)
	}
}

// uv puts the interpreter in bin/python everywhere except Windows, which uses
// Scripts/python.exe. Only the POSIX path was checked, so the Windows
// instructions in the README produced an environment the program then reported
// as missing.
func TestVenvPythonFindsEitherLayout(t *testing.T) {
	// POSIX layout.
	root := t.TempDir()
	posix := filepath.Join(root, "models", ".venv", "bin")
	if err := os.MkdirAll(posix, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(posix, "python"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := venvPython(root); got != filepath.Join(posix, "python") {
		t.Errorf("POSIX layout: got %s", got)
	}

	// Windows layout.
	root = t.TempDir()
	win := filepath.Join(root, "models", ".venv", "Scripts")
	if err := os.MkdirAll(win, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(win, "python.exe"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := venvPython(root); got != filepath.Join(win, "python.exe") {
		t.Errorf("Windows layout: got %s", got)
	}

	// Neither: name the POSIX path, which is what nearly every reader will want.
	root = t.TempDir()
	if got := venvPython(root); !strings.HasSuffix(got, filepath.Join("bin", "python")) {
		t.Errorf("with no environment present, got %s", got)
	}
}

// The report holds the same campaign names and spend figures the database does,
// and the database is deliberately kept at 0600. os.Create would have left the
// page world-readable under the usual umask.
func TestReportIsNotWorldReadable(t *testing.T) {
	days := []string{"2026-03-01"}
	data := &Data{
		Days:  []string{"2026-02-26", "2026-02-27", "2026-02-28"},
		Names: []string{"Cost"}, Entities: []string{AccountEntity},
		Percent: map[string]bool{},
		Values:  map[string]map[string][]float64{AccountEntity: {"Cost": {1, 2, 3}}},
	}
	run := Run{ID: "r", SeriesID: "s", Model: "chronos2", Horizon: 1,
		Metrics: []string{"Cost"}, Entities: data.Entities, AsOf: "2026-02-28",
		CreatedAt: time.Now(), InputHash: "h", ModelInfo: []byte(`{}`)}
	values := map[string][][][]float64{AccountEntity: {{{1, 2, 3}}}}

	path := filepath.Join(t.TempDir(), "r.html")
	if err := writeReport(path, run, data, days, []float64{0.1, 0.5, 0.9}, values, 90); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("report is %04o, want no group or other access", mode)
	}
}

// A campaign excluded for having no activity is still in the file. Reporting it
// as "no campaign named" sends the reader hunting for a typo they did not make.
func TestSelectingAnExcludedCampaignExplainsWhy(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Clicks\n")
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < smallestUsefulSeries+4; i++ {
		d := day.AddDate(0, 0, i).Format("2006-01-02")
		fmt.Fprintf(&b, "%s,Live,%d,%d\n%s,Dormant,0,0\n", d, 100+i, 5+i, d)
	}
	p := filepath.Join(dir, "c.csv")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readCSV(p, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Inactive) != 1 || data.Inactive[0] != "Dormant" {
		t.Fatalf("expected Dormant to be excluded, got %v", data.Inactive)
	}
	for _, e := range data.Entities {
		if strings.EqualFold(e, "Dormant") {
			t.Fatal("an excluded campaign is still listed as forecastable")
		}
	}
}

// A column can be present and still not forecastable: a setting you chose, or a
// label like Campaign ID. Reporting those as "no column named" sends the reader
// looking for a typo instead of the rule that excluded them.
func TestColumnsFlagNamesTheRealReason(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("Day,Campaign,Budget,Campaign ID,Cost\n")
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < smallestUsefulSeries+4; i++ {
		d := day.AddDate(0, 0, i).Format("2006-01-02")
		fmt.Fprintf(&b, "%s,Alpha,25.00,111,%d\n%s,Beta,30.00,222,%d\n", d, 100+i, d, 200+i)
	}
	p := filepath.Join(dir, "c.csv")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ column, want string }{
		{"Budget", "is a setting"},
		{"Campaign ID", "looks like a label"},
		{"Campaign", "holds text"},
		{"Nope", "no column named"},
	} {
		_, err := readCSV(p, []string{c.column}, "")
		if err == nil {
			t.Errorf("-columns %q was accepted", c.column)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("-columns %q: %v\n  want it to say %q", c.column, err, c.want)
		}
	}
}
