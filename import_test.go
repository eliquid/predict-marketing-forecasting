package main

// The import workflow: the data folder, the history rule, and the comparison
// report that several models share.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A forecast from too little history is confident nonsense, so the import
// refuses it -- and says what a good length looks like rather than only what is
// wrong with this one.
func TestEnoughHistory(t *testing.T) {
	for _, n := range []int{0, 1, 31, 89} {
		err := enoughHistory(n)
		if err == nil {
			t.Errorf("%d days was accepted", n)
			continue
		}
		for _, want := range []string{"90", "365", "730"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%d days: the refusal does not mention %s: %v", n, want, err)
			}
		}
	}
	for _, n := range []int{90, 200, 365, 1000} {
		if err := enoughHistory(n); err != nil {
			t.Errorf("%d days was refused: %v", n, err)
		}
	}
}

// The verdict is the encouragement half of the same rule.
func TestHistoryVerdict(t *testing.T) {
	for _, c := range []struct {
		days int
		want string
	}{
		{90, "would be better"},
		{364, "would be better"},
		{365, "annual seasonality"},
		{730, "two years"},
		{900, "two years"},
	} {
		if got := historyVerdict(c.days); !strings.Contains(got, c.want) {
			t.Errorf("%d days: %q, want it to mention %q", c.days, got, c.want)
		}
	}
}

// An absent data folder is created with a note in it, because the first thing
// someone runs is the thing that tells them where files go.
func TestPendingFilesCreatesTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	_, err := pendingFiles(dir)
	if err == nil {
		t.Fatal("an absent folder was not reported")
	}
	if !strings.Contains(err.Error(), "created") {
		t.Errorf("did not say it created the folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, importedName)); err != nil {
		t.Errorf("imported/ was not created: %v", err)
	}
	note, err := os.ReadFile(filepath.Join(dir, "README.txt"))
	if err != nil {
		t.Fatalf("no note left in the folder: %v", err)
	}
	for _, want := range []string{"predictmarketing import", "90 days"} {
		if !strings.Contains(string(note), want) {
			t.Errorf("the note does not mention %q", want)
		}
	}
}

// Only CSVs, never the imported/ subfolder, and never a dotfile the operating
// system left behind.
func TestPendingFilesPicksOnlyNewCSVs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, importedName), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"b.csv", "a.csv", "notes.txt", ".DS_Store", "README.txt",
		filepath.Join(importedName, "old.csv"),
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := pendingFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.csv"), filepath.Join(dir, "b.csv")}
	if len(got) != len(want) {
		t.Fatalf("picked up %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: %s, want %s", i, got[i], want[i])
		}
	}
}

// A file that has been read moves out of the folder, and a second file of the
// same name never overwrites the first.
func TestFileAwayKeepsBothImports(t *testing.T) {
	dir := t.TempDir()
	write := func() string {
		p := filepath.Join(dir, "export.csv")
		if err := os.WriteFile(p, []byte("first"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	one, err := fileAway(write(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "export.csv")); err == nil {
		t.Error("the file is still in the folder after being filed away")
	}

	two, err := fileAway(write(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("the second import overwrote the first")
	}
	for _, p := range []string{one, two} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is missing: %v", p, err)
		}
	}
}

// The two reports are named so it is obvious which is which, and both land in
// data/reports/ rather than beside the export -- data/ is meant to show, at a
// glance, what has not been imported yet.
func TestReportPaths(t *testing.T) {
	first := reportPath("data", "data/Campaign report.csv", "models")
	second := reportPath("data", "data/Campaign report.csv", "with-finetune")
	if first == second {
		t.Fatal("both reports would be written to the same file")
	}
	for _, p := range []string{first, second} {
		if !strings.HasSuffix(p, ".html") {
			t.Errorf("%s is not an html file", p)
		}
		if strings.Contains(filepath.Base(p), ".csv") {
			t.Errorf("%s kept the csv extension", p)
		}
		if filepath.Dir(p) != filepath.Join("data", reportsName) {
			t.Errorf("%s is not in data/%s", p, reportsName)
		}
		if !strings.Contains(filepath.Base(p), "Campaign report") {
			t.Errorf("%s lost the name of the export it came from", p)
		}
	}
}

// fakeRun builds a stored run without needing a model process.
func fakeRun(model string, entities, metrics []string, days int, base float64,
	trainedThrough string) forecastRun {

	values := map[string][][][]float64{}
	for _, e := range entities {
		per := make([][][]float64, len(metrics))
		for m := range metrics {
			per[m] = make([][]float64, days)
			for d := 0; d < days; d++ {
				v := base + float64(d) + float64(m)
				per[m][d] = []float64{v * 0.9, v, v * 1.1}
			}
		}
		values[e] = per
	}
	info := []byte(`{"model":"` + model + `"}`)
	if trainedThrough != "" {
		info, _ = json.Marshal(map[string]string{
			"model": model, "trained_through": trainedThrough,
		})
	}
	return forecastRun{
		Run: Run{ID: model, SeriesID: "s", Model: model, Horizon: days,
			Metrics: metrics, Entities: entities, AsOf: "2026-03-10",
			CreatedAt: time.Now(), ModelInfo: info},
		Shake:  Handshake{Repo: "vendor/" + model, Revision: "abc123def456", Quantiles: []float64{0.1, 0.5, 0.9}},
		Values: values,
	}
}

func comparisonFixture(t *testing.T, entities, metrics []string) (*Data, []string) {
	t.Helper()
	data := &Data{
		Days:     []string{"2026-03-08", "2026-03-09", "2026-03-10"},
		Names:    metrics,
		Entities: entities,
		GroupBy:  "Campaign",
		Percent:  map[string]bool{},
		Values:   map[string]map[string][]float64{},
	}
	for _, e := range entities {
		data.Values[e] = map[string][]float64{}
		for _, m := range metrics {
			data.Values[e][m] = []float64{10, 11, 12}
		}
	}
	return data, []string{"2026-03-11", "2026-03-12"}
}

// Every model's median has to reach the page, on the same axes, for every
// campaign and every metric.
func TestComparisonReportHasEveryModelAndPane(t *testing.T) {
	entities := []string{AccountEntity, "Brand", "Shopping"}
	metrics := []string{"Cost", "Clicks"}
	data, days := comparisonFixture(t, entities, metrics)
	runs := []forecastRun{
		fakeRun("chronos2", entities, metrics, len(days), 100, ""),
		fakeRun("timesfm3", entities, metrics, len(days), 200, ""),
		fakeRun("chronos2ft", entities, metrics, len(days), 300, "2026-03-10"),
	}

	path := filepath.Join(t.TempDir(), "c.html")
	if err := writeComparison(path, runs, data, days, 90); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)

	if n := strings.Count(page, `class="pane"`); n != len(entities)*len(metrics) {
		t.Errorf("%d panes, want %d", n, len(entities)*len(metrics))
	}
	for _, m := range runs {
		if !strings.Contains(page, m.Run.Model) {
			t.Errorf("model %q is missing from the page", m.Run.Model)
		}
	}
	for _, e := range entities {
		if !strings.Contains(page, `data-entity="`+e+`"`) {
			t.Errorf("no pane for entity %q", e)
		}
	}
	for _, m := range metrics {
		if !strings.Contains(page, `data-metric="`+m+`"`) {
			t.Errorf("no pane for metric %q", m)
		}
	}
	// Each model needs its own colour, or the lines cannot be told apart.
	for i := range runs {
		if !strings.Contains(page, colourFor(i)) {
			t.Errorf("colour %s for model %d never appears", colourFor(i), i)
		}
	}
	// The fine-tuned model must declare what it was trained on.
	if !strings.Contains(page, "2026-03-10") {
		t.Error("the fine-tuned cutoff is not shown")
	}
	// Both dropdowns, and the script that drives them.
	for _, want := range []string{`id="pick-entity"`, `id="pick-metric"`, "addEventListener"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Private, like every other artefact that names real campaigns.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("comparison report is %04o, want no group or other access", mode)
	}
}

// Report 1 has two models and report 2 has three. The same code writes both, so
// it must not assume a fixed number.
func TestComparisonWorksWithTwoAndThreeModels(t *testing.T) {
	entities := []string{AccountEntity}
	metrics := []string{"Cost"}
	data, days := comparisonFixture(t, entities, metrics)

	for _, n := range []int{1, 2, 3} {
		var runs []forecastRun
		for i := 0; i < n; i++ {
			runs = append(runs, fakeRun(fmt.Sprintf("model%d", i), entities, metrics,
				len(days), float64(100*(i+1)), ""))
		}
		path := filepath.Join(t.TempDir(), "c.html")
		if err := writeComparison(path, runs, data, days, 90); err != nil {
			t.Fatalf("%d models: %v", n, err)
		}
		b, _ := os.ReadFile(path)
		if got := strings.Count(string(b), "<th>model"); got != 0 && n == 0 {
			t.Errorf("unexpected header count %d", got)
		}
		for i := 0; i < n; i++ {
			if !strings.Contains(string(b), fmt.Sprintf("model%d", i)) {
				t.Errorf("%d models: model%d missing", n, i)
			}
		}
	}
}

// A model that forecast a campaign the others did not must not put a dropdown
// entry on the page that only one line can answer.
func TestComparisonUsesOnlySharedSeries(t *testing.T) {
	metrics := []string{"Cost"}
	data, days := comparisonFixture(t, []string{AccountEntity, "Brand", "Extra"}, metrics)

	both := fakeRun("chronos2", []string{AccountEntity, "Brand"}, metrics, len(days), 100, "")
	extra := fakeRun("timesfm3", []string{AccountEntity, "Brand", "Extra"}, metrics, len(days), 200, "")

	path := filepath.Join(t.TempDir(), "c.html")
	if err := writeComparison(path, []forecastRun{both, extra}, data, days, 90); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `data-entity="Extra"`) {
		t.Error("a campaign only one model forecast was offered for comparison")
	}
	if !strings.Contains(string(b), `data-entity="Brand"`) {
		t.Error("a campaign both models forecast is missing")
	}
}

// The account is what the page should open on, not whichever campaign happened
// to sort first.
func TestAccountComesFirstInTheDropdown(t *testing.T) {
	metrics := []string{"Cost"}
	entities := []string{"Brand", AccountEntity, "Shopping"}
	runs := []forecastRun{fakeRun("chronos2", entities, metrics, 2, 100, "")}
	if got := sharedEntities(runs); got[0] != AccountEntity {
		t.Errorf("first entity is %q, want %q", got[0], AccountEntity)
	}
}

// trainedThrough is what keeps a fine-tuned model from being judged on days it
// was trained on, so it has to survive the round trip through the handshake.
func TestTrainedThroughIsReadFromTheHandshake(t *testing.T) {
	if got := trainedThrough([]byte(`{"trained_through":"2026-01-04"}`)); got != "2026-01-04" {
		t.Errorf("got %q", got)
	}
	for _, in := range []string{``, `{}`, `not json`, `{"trained_through":5}`} {
		if got := trainedThrough([]byte(in)); got != "" {
			t.Errorf("%q gave %q, want empty", in, got)
		}
	}
}

// Two models that agree finish at nearly the same height, so their names would
// print on top of each other exactly when the chart is telling you something
// worth reading.
func TestSpreadLabelsSeparatesAndStaysInBounds(t *testing.T) {
	const top, bottom, gap = 20.0, 300.0, 15.0

	// three lines finishing within a pixel of each other
	got := spreadLabels([]lineEnd{
		{y: 150, name: "a"}, {y: 150.5, name: "b"}, {y: 151, name: "c"},
	}, gap, top, bottom)
	if len(got) != 3 {
		t.Fatalf("got %d labels, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if d := got[i].labelY - got[i-1].labelY; d < gap-0.01 {
			t.Errorf("labels %d and %d are %.1f apart, want at least %.0f", i-1, i, d, gap)
		}
	}
	for _, e := range got {
		if e.labelY < top-0.01 || e.labelY > bottom+0.01 {
			t.Errorf("label %q at %.1f is outside the plot (%.0f..%.0f)", e.name, e.labelY, top, bottom)
		}
	}

	// a crowd at the very bottom must be lifted, not pushed off the chart
	var crowd []lineEnd
	for i := 0; i < 5; i++ {
		crowd = append(crowd, lineEnd{y: bottom, name: "m"})
	}
	for _, e := range spreadLabels(crowd, gap, top, bottom) {
		if e.labelY > bottom+0.01 || e.labelY < top-0.01 {
			t.Errorf("crowded label at %.1f escaped the plot", e.labelY)
		}
	}

	// one line needs no adjustment beyond sitting above its own end point
	if one := spreadLabels([]lineEnd{{y: 100, name: "solo"}}, gap, top, bottom); one[0].labelY >= 100 {
		t.Errorf("single label at %.1f, want above the end point", one[0].labelY)
	}
	if none := spreadLabels(nil, gap, top, bottom); len(none) != 0 {
		t.Error("spreadLabels invented a label from nothing")
	}
}

// models/ has always been found relative to the binary, but data/ and pm.db were
// plain relative paths. Running the program from another directory therefore made
// a second empty data/ there and a second database beside it, while still loading
// the models correctly -- which is exactly what made it look like it had worked.
// Forecasts split across databases cannot be scored against later actuals.
func TestDefaultsAnchorToTheInstallNotTheShell(t *testing.T) {
	root := installDir()
	if root == "" || root == "." {
		t.Skip("not running from an installation; nothing to anchor to")
	}

	for _, name := range []string{"pm.db", importDir} {
		got := defaultPath(name)
		if !filepath.IsAbs(got) {
			t.Errorf("defaultPath(%q) = %q, want an absolute path", name, got)
		}
		if filepath.Dir(got) != root {
			t.Errorf("defaultPath(%q) = %q, want it inside %q", name, got, root)
		}
	}

	// The same answer wherever the process happens to be standing.
	before := defaultPath("pm.db")
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	if after := defaultPath("pm.db"); after != before {
		t.Errorf("the default database moved with the shell: %q then %q", before, after)
	}
}

// finetune.py once took a --budget wall clock, and import passed 600 seconds. On
// a real export that ended training at step 1,210 of 2,000 -- 0.605 of an epoch
// -- and the adapter then appeared in every report as a peer of the pretrained
// models with nothing saying it had been cut short. Nobody asked for that limit.
// If training is too slow the honest lever is --steps.
func TestTrainingIsNeverTimeLimited(t *testing.T) {
	py, err := os.ReadFile(filepath.Join("models", "finetune.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"--budget", "hit_time_budget", "should_training_stop"} {
		if strings.Contains(string(py), gone) {
			t.Errorf("finetune.py still contains %q: training must run every step it "+
				"was asked for", gone)
		}
	}

	src, err := os.ReadFile("import.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "--budget") {
		t.Error("import.go passes a time budget to the trainer")
	}
	if !strings.Contains(string(src), `"--steps"`) {
		t.Error("import.go no longer tells the trainer how many steps to run")
	}
}

// The first crosshair put its readout inside the scrolling container, so it
// scrolled off with the content and showed nothing in practice while looking
// fine to a synthetic mousemove. It has to live outside the scroller, and every
// series has to be addressable so the legend can hide it.
func TestChartIsInteractive(t *testing.T) {
	entities := []string{AccountEntity, "Brand"}
	metrics := []string{"Cost"}
	data, days := comparisonFixture(t, entities, metrics)
	runs := []forecastRun{
		fakeRun("chronos2", entities, metrics, len(days), 100, ""),
		fakeRun("timesfm3", entities, metrics, len(days), 200, ""),
	}
	path := filepath.Join(t.TempDir(), "c.html")
	if err := writeComparison(path, runs, data, days, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)

	// The readout must come before the scroller, i.e. not be nested in it.
	ro, sc := strings.Index(page, `class="readout"`), strings.Index(page, `class="scroller"`)
	if ro < 0 || sc < 0 {
		t.Fatal("the page has no readout or no scroller")
	}
	if ro > sc {
		t.Error("the readout is inside the scroller again; it will scroll out of view")
	}

	// Every series, including the observed history, is togglable by name.
	for _, model := range []string{"__actual", "chronos2", "timesfm3"} {
		if !strings.Contains(page, `data-model="`+model+`"`) {
			t.Errorf("no element tagged for series %q", model)
		}
		if !strings.Contains(page, `class="key" data-model="`+model+`"`) {
			t.Errorf("no legend button for series %q", model)
		}
	}

	// The crosshair needs the y projection to put markers on the lines.
	for _, key := range []string{`"y0":`, `"y1":`, `"top":`, `"bottom":`, `"xs":`, `"cut":`} {
		if !strings.Contains(page, key) {
			t.Errorf("the series data is missing %s", key)
		}
	}
	// A fixed pixel width is what makes pointer position a chart coordinate.
	if !strings.Contains(page, `<svg width="`) {
		t.Error("the chart is no longer emitted at a fixed width; the crosshair maths will not hold")
	}
}
