package main

// Round 12: the report is one file that has to keep working after it is moved,
// copied or emailed.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func sampleReport(t *testing.T, horizon int) string {
	t.Helper()
	data := &Data{Values: map[string]map[string][]float64{AccountEntity: {}},
		Entities: []string{AccountEntity}}
	for i := 0; i < 40; i++ {
		data.Days = append(data.Days, day(i))
	}
	data.Names = []string{"spend", "clicks"}
	for _, n := range data.Names {
		col := make([]float64, 40)
		for i := range col {
			col[i] = float64(100 + i)
		}
		data.Values[AccountEntity][n] = col
	}
	days, err := nextDays(day(39), horizon)
	if err != nil {
		t.Fatal(err)
	}
	per := make([][][]float64, len(data.Names))
	for m := range per {
		per[m] = make([][]float64, horizon)
		for i := range per[m] {
			per[m][i] = []float64{140, 145, 150}
		}
	}
	q := map[string][][][]float64{AccountEntity: per}
	path := filepath.Join(t.TempDir(), "r.html")
	run := Run{ID: "abc", SeriesID: "s", Model: "chronos2", Horizon: horizon,
		Metrics: data.Names, Entities: []string{AccountEntity},
		CreatedAt: time.Now(), InputHash: "hash",
		ModelInfo: []byte(`{"repo":"r"}`)}
	if err := writeReport(path, run, data, days, []float64{0.1, 0.5, 0.9}, q, 90); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The report used to reference htmx.min.js as a sibling file, so every report
// written outside testdata/ had a dangling script tag.
func TestReportHasNoExternalReferences(t *testing.T) {
	h := sampleReport(t, 3)
	for _, bad := range []string{`src="`, "http://", "https://", "//cdn"} {
		if strings.Contains(h, bad) {
			t.Errorf("report must be self-contained, found %q", bad)
		}
	}
	if !strings.Contains(h, strings.TrimSpace(htmxJS)) {
		t.Error("htmx should be inlined in full")
	}
}

// Inlining a script is only safe while the script contains no "</script".
// A future htmx upgrade that did would silently truncate every report.
func TestEmbeddedScriptCannotBreakOutOfItsTag(t *testing.T) {
	if strings.Contains(strings.ToLower(htmxJS), "</script") {
		t.Fatal("embedded htmx contains </script and would break the page when inlined")
	}
}

func TestReportStructureIsBalanced(t *testing.T) {
	h := sampleReport(t, 3)
	// Compare only the page, not the inlined library, whose source contains
	// harmless string literals like "</style".
	before, rest, _ := strings.Cut(h, "<script>")
	_, after, _ := strings.Cut(rest, "</script>")
	page := before + after
	for _, tag := range []string{"html", "head", "body", "main", "table", "svg", "style"} {
		o := len(regexp.MustCompile(`<`+tag+`[\s>]`).FindAllString(page, -1))
		c := strings.Count(page, "</"+tag+">")
		if o != c {
			t.Errorf("<%s>: %d open, %d close", tag, o, c)
		}
	}
}

func TestReportWorksAtHorizonOne(t *testing.T) {
	h := sampleReport(t, 1)
	if !strings.Contains(h, "<svg") {
		t.Error("chart missing at horizon 1")
	}
	if n := strings.Count(h, "<tr><td>"); n != 2 { // one row per metric
		t.Errorf("got %d table rows, want 2", n)
	}
}

func TestReportEscapesTheSeriesName(t *testing.T) {
	data := &Data{Names: []string{"spend"}, Entities: []string{AccountEntity}, Values: map[string]map[string][]float64{AccountEntity: {}}}
	col := make([]float64, 40)
	for i := 0; i < 40; i++ {
		data.Days = append(data.Days, day(i))
		col[i] = float64(100 + i)
	}
	data.Values[AccountEntity]["spend"] = col
	days, _ := nextDays(day(39), 1)
	path := filepath.Join(t.TempDir(), "r.html")
	run := Run{ID: "a", SeriesID: `<script>alert(1)</script>`, Model: "m", Horizon: 1,
		Metrics: []string{"spend"}, Entities: []string{AccountEntity},
		CreatedAt: time.Now(), ModelInfo: []byte(`{}`)}
	if err := writeReport(path, run, data, days, []float64{0.1, 0.5, 0.9},
		map[string][][][]float64{AccountEntity: {{{1, 2, 3}}}}, 90); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "<script>alert(1)") {
		t.Error("series name was not escaped")
	}
}

// Both models used to write to the same default file, so running one after the
// other destroyed the first report -- precisely when comparing them.
func TestDefaultReportNameIncludesTheModel(t *testing.T) {
	seen := map[string]bool{}
	for _, model := range []string{"timesfm3", "chronos2"} {
		name := strings.TrimSuffix("data.csv", ".csv") + "_forecast_" + model + ".html"
		if seen[name] {
			t.Fatalf("two models share the default report name %q", name)
		}
		seen[name] = true
		if !strings.Contains(name, model) {
			t.Errorf("%q should name the model", name)
		}
	}
}

// -entities can pick campaigns without the account total, which used to crash
// the page with a nil pointer on the summary table's header.
func TestReportWorksWithoutTheAccountTotal(t *testing.T) {
	data := &Data{
		Names:    []string{"Cost"},
		Entities: []string{"Brand"},
		Values:   map[string]map[string][]float64{"Brand": {}},
	}
	col := make([]float64, 40)
	for i := 0; i < 40; i++ {
		data.Days = append(data.Days, day(i))
		col[i] = float64(100 + i)
	}
	data.Values["Brand"]["Cost"] = col

	days, _ := nextDays(day(39), 2)
	run := Run{ID: "a", SeriesID: "s", Model: "m", Horizon: 2,
		Metrics: []string{"Cost"}, Entities: []string{"Brand"}, // no account
		CreatedAt: time.Now(), ModelInfo: []byte(`{}`)}
	path := filepath.Join(t.TempDir(), "r.html")
	err := writeReport(path, run, data, days, []float64{0.1, 0.5, 0.9},
		map[string][][][]float64{"Brand": {{{1, 2, 3}, {4, 5, 6}}}}, 90)
	if err != nil {
		t.Fatalf("a report without the account total must still render: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "Brand") {
		t.Error("the campaign is missing from the page")
	}
	if strings.Contains(string(b), AccountEntity) {
		t.Error("the account was not asked for and should not appear")
	}
}

// A campaign that was paused for the whole period has nothing to forecast, so it
// is excluded. The terminal said so and the report did not, which made a campaign
// silently absent from the copy people actually pass around.
func TestReportNamesExcludedCampaigns(t *testing.T) {
	days := []string{"2026-03-01", "2026-03-02"}
	data := &Data{
		Days:     []string{"2026-02-26", "2026-02-27", "2026-02-28"},
		Names:    []string{"Cost"},
		Entities: []string{AccountEntity, "Live"},
		Inactive: []string{"Paused One", "Paused Two"},
		GroupBy:  "Campaign",
		Percent:  map[string]bool{},
		Values: map[string]map[string][]float64{
			AccountEntity: {"Cost": {1, 2, 3}},
			"Live":        {"Cost": {1, 2, 3}},
		},
	}
	run := Run{ID: "r", SeriesID: "s", Model: "chronos2", Horizon: len(days),
		Metrics: []string{"Cost"}, Entities: []string{AccountEntity, "Live"},
		AsOf: "2026-02-28", CreatedAt: time.Now(), InputHash: "h",
		ModelInfo: []byte(`{}`)}
	values := map[string][][][]float64{
		AccountEntity: {{{1, 2, 3}, {2, 3, 4}}},
		"Live":        {{{1, 2, 3}, {2, 3, 4}}},
	}

	path := filepath.Join(t.TempDir(), "r.html")
	if err := writeReport(path, run, data, days, []float64{0.1, 0.5, 0.9}, values, 90); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(page)
	for _, want := range []string{"Paused One", "Paused Two", "switched off", "still stored"} {
		if !strings.Contains(got, want) {
			t.Errorf("report does not mention %q", want)
		}
	}

	// With nothing excluded there must be no note at all, rather than an empty one.
	data.Inactive = nil
	if err := writeReport(path, run, data, days, []float64{0.1, 0.5, 0.9}, values, 90); err != nil {
		t.Fatal(err)
	}
	page, _ = os.ReadFile(path)
	if strings.Contains(string(page), "switched off") {
		t.Error("the exclusion note appears even though nothing was excluded")
	}
}
