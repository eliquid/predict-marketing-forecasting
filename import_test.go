package main

// The import workflow: the data folder, the history rule, and the comparison
// report that several models share.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

	// install.sh prints a command for the user to copy, and it went on printing
	// `--budget 600` for three commits after the flag was deleted -- so the very
	// first thing a new user was told to run failed with "unrecognized
	// arguments". Anywhere that quotes the trainer's command line counts.
	sh, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sh), "--budget") {
		t.Error("install.sh tells the user to pass --budget, which no longer exists")
	}
}

// Nor may it stop, shorten or editorialise because it dislikes the data. The
// trainer once printed a warning when a file had fewer than 50 series: it could
// not act on it, the reader could not act on it either, and in the middle of a
// successful run it read as a failure. Whether the adapter helps is a question
// for `accuracy`, on days the model never saw -- not a guess made beforehand.
func TestTrainingIsNeverGatedOnTheData(t *testing.T) {
	py, err := os.ReadFile(filepath.Join("models", "finetune.py"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(py)
	for _, gone := range []string{"very little", "WORSE", "< 50", "NOTE:"} {
		if strings.Contains(src, gone) {
			t.Errorf("finetune.py still passes judgement on the data (%q). It runs the "+
				"steps it was given and says what it did.", gone)
		}
	}
	// What matters is not how many ways out there are -- refusing unreadable
	// input before training starts is the project's first hard rule -- but that
	// none of them can fire once training is under way. Everything before
	// `pipe.fit` is validation; after it, the only exit is the adapter check.
	fit := strings.Index(src, "pipe.fit(")
	if fit < 0 {
		t.Fatal("finetune.py no longer calls pipe.fit; this test is checking nothing")
	}
	if n := strings.Count(src[fit:], "sys.exit"); n != 1 {
		t.Errorf("finetune.py has %d exits after training begins, want only the "+
			"adapter check; training must never be stopped part-way", n)
	}
	// And nothing may end it from inside a callback, which is how the wall clock
	// did it.
	if i := strings.Index(src, "class StepCount"); i >= 0 {
		body := src[i:]
		if end := strings.Index(body, "\n    from "); end > 0 {
			body = body[:end]
		}
		for _, banned := range []string{"sys.exit", "should_training_stop", "raise"} {
			if strings.Contains(body, banned) {
				t.Errorf("the StepCount callback contains %q; it counts steps and "+
					"nothing else", banned)
			}
		}
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

// Every import forecasts three windows, and a window longer than the file is
// skipped rather than refused -- a short export still produces everything it can.
func TestWindowSelection(t *testing.T) {
	// Which windows a file of n days should actually produce a run for.
	for _, c := range []struct {
		days int
		want []string
	}{
		{90, []string{"full"}},                 // the 90d window IS the file
		{91, []string{"full", "90d"}},          // one day longer, so both differ
		{100, []string{"full", "90d"}},         // 270 out of reach
		{269, []string{"full", "90d"}},         // still out of reach
		{270, []string{"full", "90d"}},         // the 270d window IS the file
		{271, []string{"full", "270d", "90d"}}, // the first length that gives all three
		{1099, []string{"full", "270d", "90d"}},
	} {
		var got []string
		for _, w := range importWindows {
			if w.days > c.days || (w.days > 0 && w.days == c.days) {
				continue
			}
			got = append(got, w.label)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%d days: windows %v, want %v", c.days, got, c.want)
		}
	}
}

// lastDays trims the numbers and nothing else. Every window has to forecast the
// same campaigns and metrics, or writeComparison intersects them across runs and
// quietly drops any campaign a shorter window classified differently.
func TestLastDaysTrimsOnlyTheNumbers(t *testing.T) {
	full := &Data{
		Days:     []string{"2026-01-01", "2026-01-02", "2026-01-03", "2026-01-04"},
		Names:    []string{"Cost", "Clicks"},
		Entities: []string{AccountEntity, "Brand"},
		GroupBy:  "Campaign",
		Inactive: []string{"Dead"},
		Paused:   []string{"Dead"},
		Values: map[string]map[string][]float64{
			AccountEntity: {"Cost": {1, 2, 3, 4}, "Clicks": {10, 20, 30, 40}},
			"Brand":       {"Cost": {5, 6, 7, 8}, "Clicks": {50, 60, 70, 80}},
		},
	}

	cut := lastDays(full, 2)
	if len(cut.Days) != 2 || cut.Days[0] != "2026-01-03" {
		t.Fatalf("days = %v, want the last two", cut.Days)
	}
	if got := cut.Values[AccountEntity]["Cost"]; len(got) != 2 || got[0] != 3 {
		t.Errorf("account Cost = %v, want [3 4]", got)
	}
	if got := cut.Values["Brand"]["Clicks"]; len(got) != 2 || got[0] != 70 {
		t.Errorf("Brand Clicks = %v, want [70 80]", got)
	}
	for _, f := range []struct {
		name      string
		got, want interface{}
	}{
		{"Names", strings.Join(cut.Names, ","), strings.Join(full.Names, ",")},
		{"Entities", strings.Join(cut.Entities, ","), strings.Join(full.Entities, ",")},
		{"GroupBy", cut.GroupBy, full.GroupBy},
		{"Inactive", strings.Join(cut.Inactive, ","), strings.Join(full.Inactive, ",")},
		{"Paused", strings.Join(cut.Paused, ","), strings.Join(full.Paused, ",")},
	} {
		if f.got != f.want {
			t.Errorf("%s changed: %v, want %v", f.name, f.got, f.want)
		}
	}

	// The original must be untouched -- the full window is forecast too.
	if len(full.Days) != 4 || full.Values[AccountEntity]["Cost"][0] != 1 {
		t.Error("lastDays modified the data it was given")
	}
	// A window at or beyond the file is the file.
	if lastDays(full, 4) != full || lastDays(full, 99) != full || lastDays(full, 0) != full {
		t.Error("a window at or beyond the file length should return it unchanged")
	}
}

// The run label carries the window, because accuracy groups by the model column
// and the whole point is to find out which history length forecasts best.
func TestRunLabelCarriesTheWindow(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range importWindows {
		for _, m := range []string{"chronos2", "timesfm3"} {
			l := runLabel(m, w.label)
			if seen[l] {
				t.Errorf("%q is not unique across windows", l)
			}
			seen[l] = true
			if !strings.HasPrefix(l, m) {
				t.Errorf("%q does not start with its model", l)
			}
		}
	}
	if seen[averageLabel] {
		t.Errorf("the average label %q collides with a model label", averageLabel)
	}
}

// A file too short for the models is also too short for the import, and the
// reader has to be told the gate they must clear -- not the lower one they
// happened to trip first. Quoting 32 at someone who needs 90 sends them back
// with a file that will be refused again.
func TestAShortFileNamesTheImportGateNotTheModelFloor(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost\n")
	for i := 0; i < 20; i++ { // below both floors
		fmt.Fprintf(&b, "%s,Brand,%d\n%s,Shopping,%d\n", day(i), 100+i, day(i), 200+i)
	}
	_, err := readCSV(writeTemp(t, "short.csv", b.String()), nil, "")
	if err == nil {
		t.Fatal("a 20-day file must be refused")
	}

	// readCSV reports its own floor, and carries the count so the caller can
	// report a higher one.
	var short tooShort
	if !errors.As(err, &short) {
		t.Fatalf("the refusal is not a tooShort: %T %v", err, err)
	}
	if short.Days != 20 {
		t.Errorf("tooShort.Days = %d, want 20", short.Days)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(smallestUsefulSeries)) {
		t.Errorf("forecast's own message should name its floor: %v", err)
	}

	// What import does with it: the 90-day gate, not the 32-day one.
	imp := enoughHistory(short.Days)
	if imp == nil {
		t.Fatal("20 days must not clear the import gate")
	}
	if !strings.Contains(imp.Error(), strconv.Itoa(importMinDays)) {
		t.Errorf("the import refusal should name %d: %v", importMinDays, imp)
	}
	if strings.Contains(imp.Error(), "least 32") {
		t.Errorf("the import refusal should not send the reader to the 32-day floor: %v", imp)
	}
}

// The average is a real stored run so accuracy can score it, and it has to be
// the actual mean -- of every quantile, not just the median, so the interval
// averages too.
func TestAverageRunIsTheMeanOfEveryQuantile(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	entities := []string{AccountEntity, "Brand"}
	metrics := []string{"Cost", "Clicks"}
	days := []string{"2026-03-11", "2026-03-12"}
	runs := []forecastRun{
		fakeRun("chronos2@full", entities, metrics, len(days), 100, ""),
		fakeRun("timesfm3@full", entities, metrics, len(days), 200, ""),
	}

	avg, ok, err := averageRun("s", runs, days, len(days))
	if err != nil || !ok {
		t.Fatalf("averageRun: ok=%v err=%v", ok, err)
	}
	if avg.Run.Model != averageLabel {
		t.Errorf("stored as %q, want %q", avg.Run.Model, averageLabel)
	}
	for _, e := range entities {
		for mi := range metrics {
			for di := range days {
				for qi := range avg.Shake.Quantiles {
					want := (runs[0].Values[e][mi][di][qi] + runs[1].Values[e][mi][di][qi]) / 2
					if got := avg.Values[e][mi][di][qi]; got != want {
						t.Fatalf("%s m%d d%d q%d: %v, want %v", e, mi, di, qi, got, want)
					}
				}
			}
		}
	}

	// averageRun computes and writes nothing -- that is what lets a failing model
	// leave the database untouched. Storing is a separate step.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forecasts f JOIN runs r ON r.id=f.run_id
	                       WHERE r.model=?`, averageLabel).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("averageRun wrote %d rows; it must not write at all", n)
	}

	// And once stored deliberately, accuracy can find it.
	if err := storeRun(db, avg, days); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM forecasts f JOIN runs r ON r.id=f.run_id
	                       WHERE r.model=?`, averageLabel).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if want := len(entities) * len(metrics) * len(days) * len(avg.Shake.Quantiles); n != want {
		t.Errorf("stored %d forecast rows, want %d", n, want)
	}
}

// Averaging quantile grids that do not line up would invent numbers belonging to
// neither, so a run declaring a different grid is left out. Below two runs there
// is nothing to average and the caller gets no run at all.
func TestAverageRunRefusesMismatchedQuantiles(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	e, m := []string{AccountEntity}, []string{"Cost"}
	a := fakeRun("chronos2@full", e, m, 2, 100, "")
	odd := fakeRun("other@full", e, m, 2, 200, "")
	odd.Shake.Quantiles = []float64{0.05, 0.5, 0.95} // a different grid

	if _, ok, err := averageRun("s", []forecastRun{a, odd}, []string{"d1", "d2"}, 2); err != nil || ok {
		t.Errorf("one usable run is not an average: ok=%v err=%v", ok, err)
	}
	if _, ok, err := averageRun("s", []forecastRun{a}, []string{"d1", "d2"}, 2); err != nil || ok {
		t.Errorf("a single run is not an average: ok=%v err=%v", ok, err)
	}
}

// The model column in `accuracy` and `runs` has to be wide enough for the labels
// this code can actually produce. Adding the window made them longer, and a
// column one character short turns the whole table into ragged noise.
func TestTheModelColumnFitsEveryLabel(t *testing.T) {
	longest := len(averageLabel)
	for _, w := range importWindows {
		for _, m := range []string{"chronos2", "timesfm3", "chronos2ft"} {
			if n := len(runLabel(m, w.label)); n > longest {
				longest = n
			}
		}
	}
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	// Every width the model column is printed at, in both tables.
	found := regexp.MustCompile(`%-(\d+)s`).FindAllStringSubmatch(string(src), -1)
	widest := 0
	for _, m := range found {
		n, _ := strconv.Atoi(m[1])
		if n > widest {
			widest = n
		}
	}
	if widest < longest {
		t.Fatalf("the widest column in main.go is %d but labels reach %d characters", widest, longest)
	}
	// And specifically: the accuracy/runs model column.
	if !strings.Contains(string(src), fmt.Sprintf("%%-%ds", 16)) {
		t.Errorf("the model column is no longer printed at width 16; longest label is %d", longest)
	}
	if longest > 16 {
		t.Errorf("labels now reach %d characters -- widen the model column past 16", longest)
	}
}

// The average is always the last 90 days, whatever the file's length -- measured
// over 31 walk-forward origins, a 90-day window beat both the whole file and 270
// days at every horizon. The individual windows are still stored so `accuracy`
// can score them; they are simply not drawn.
func TestTheAverageIsAlwaysTheNinetyDayWindow(t *testing.T) {
	if averageWindow != "90d" {
		t.Fatalf("averageWindow = %q, want the 90-day window", averageWindow)
	}
	if !strings.Contains(averageLabel, "90") {
		t.Errorf("averageLabel %q should name the window it is built from", averageLabel)
	}
	// The window it names has to be one the import actually runs.
	found := false
	for _, w := range importWindows {
		if w.label == averageWindow {
			found = true
			if w.days != importMinDays {
				t.Errorf("the %q window is %d days but the import minimum is %d",
					w.label, w.days, importMinDays)
			}
		}
	}
	if !found {
		t.Errorf("averageWindow %q is not in importWindows", averageWindow)
	}
}

// A file of exactly 90 days skips the 90d window as a duplicate of the whole
// file, so there would be no 90-day runs to average. `full` is the last 90 days
// there, and the average has to fall back to it or the import produces no line
// at all on the one file length that is exactly the documented minimum.
func TestTheAverageFallsBackToFullOnAnExactlyMinimumFile(t *testing.T) {
	for _, days := range []int{importMinDays, importMinDays + 1, 400} {
		var ran []string
		for _, w := range importWindows {
			if w.days > days || (w.days > 0 && w.days == days) {
				continue
			}
			ran = append(ran, w.label)
		}
		// Which window the average would be built from, mirroring importOne.
		src := averageWindow
		if !slicesContainsFold(ran, averageWindow) {
			src = "full"
		}
		if !slicesContainsFold(ran, src) {
			t.Errorf("%d days: the average would be built from %q, which did not run (ran %v)",
				days, src, ran)
		}
		if days == importMinDays && src != "full" {
			t.Errorf("%d days: expected the fallback to full, got %q", days, src)
		}
		if days > importMinDays && src != averageWindow {
			t.Errorf("%d days: expected the %s window, got %q", days, averageWindow, src)
		}
	}
}

// The q10-q90 band. The comparison page drew none for as long as it carried six
// model lines, where six overlapping bands really are unreadable. It draws one
// or two now, so the interval the models actually returned is legible and worth
// having -- the nine quantiles were always stored, just never rendered.
func TestTheComparisonChartDrawsTheUncertaintyBand(t *testing.T) {
	entities := []string{AccountEntity, "Brand"}
	metrics := []string{"Cost"}
	data, days := comparisonFixture(t, entities, metrics)
	runs := []forecastRun{fakeRun("average@90d", entities, metrics, len(days), 100, "")}

	path := filepath.Join(t.TempDir(), "band.html")
	if err := writeComparison(path, runs, data, days, 90); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)

	// One band per pane, translucent, and inside the series group so hiding the
	// line hides its band too.
	if n := strings.Count(page, "fill-opacity="); n != len(entities)*len(metrics) {
		t.Errorf("%d bands, want one per pane (%d)", n, len(entities)*len(metrics))
	}
	if !strings.Contains(page, "<polygon fill=") {
		t.Error("the band is not drawn as a filled polygon")
	}
	for _, op := range regexp.MustCompile(`fill-opacity="([0-9.]+)"`).FindAllStringSubmatch(page, -1) {
		v, _ := strconv.ParseFloat(op[1], 64)
		if v <= 0 || v >= 1 {
			t.Errorf("fill-opacity %v is not translucent", v)
		}
	}
	// The band must be inside the group the legend toggles, before the median so
	// the line sits on top of it.
	gi := strings.Index(page, `<g class="series" data-model="average@90d">`)
	if gi < 0 {
		t.Fatal("no series group for the average")
	}
	grp := page[gi:]
	if end := strings.Index(grp, "</g>"); end > 0 {
		grp = grp[:end]
	}
	poly := strings.Index(grp, "<polygon")
	line := strings.Index(grp, "<polyline")
	if poly < 0 || line < 0 || poly > line {
		t.Error("the band must be drawn inside the series group, before the median line")
	}

	// A model that returned a single quantile has no interval to draw.
	flat := fakeRun("solo@90d", entities, metrics, len(days), 100, "")
	for e := range flat.Values {
		for mi := range flat.Values[e] {
			for di := range flat.Values[e][mi] {
				flat.Values[e][mi][di] = flat.Values[e][mi][di][:1]
			}
		}
	}
	flat.Shake.Quantiles = []float64{0.5}
	p2 := filepath.Join(t.TempDir(), "noband.html")
	if err := writeComparison(p2, []forecastRun{flat}, data, days, 90); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p2)
	if strings.Contains(string(b2), "fill-opacity=") {
		t.Error("a single-quantile forecast has no band to draw")
	}
}

// reversePoints closes the band polygon; if it stopped reversing, the shape
// would cross itself into a bow-tie instead of a filled ribbon.
func TestReversePoints(t *testing.T) {
	if got := reversePoints("1,2 3,4 5,6"); got != "5,6 3,4 1,2" {
		t.Errorf("reversePoints = %q", got)
	}
	if got := reversePoints(""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

// Nothing is destroyed until every model has answered.
//
// `import` empties the database before storing, and for an hour it did so
// *before* running the models -- so a model that refused halfway left the user
// with their old forecasts gone, a partial set of new ones and no report.
// Measured at the time: worse than before they ran the command. forecastModel
// and storeRun are split so the whole job is computed first and committed once.
func TestForecastingWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	entities := []string{AccountEntity, "Brand"}
	metrics := []string{"Cost"}
	days := []string{"2026-03-11", "2026-03-12"}

	// A forecast that exists only in memory leaves no trace.
	r := fakeRun("chronos2@90d", entities, metrics, len(days), 100, "")
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the database starts with %d runs; the test proves nothing", n)
	}

	// Averaging is also read-only.
	r2 := fakeRun("timesfm3@90d", entities, metrics, len(days), 200, "")
	avg, ok, err := averageRun("s", []forecastRun{r, r2}, days, len(days))
	if err != nil || !ok {
		t.Fatalf("averageRun: ok=%v err=%v", ok, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("averaging wrote %d runs; forecasting must not touch the database", n)
	}

	// Only storeRun writes.
	for _, run := range []forecastRun{r, r2, avg} {
		if err := storeRun(db, run, days); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("stored %d runs, want 3", n)
	}
}

// A failed model must be attributable to its window. The same model runs over
// three of them, and "timesfm3 refused" does not say which one.
func TestAFailedModelNamesItsWindow(t *testing.T) {
	_, err := forecastModel("nosuchmodel", runLabel("nosuchmodel", "270d"), "s",
		&Data{Days: []string{"2026-01-01"}, Names: []string{"Cost"},
			Entities: []string{AccountEntity},
			Values:   map[string]map[string][]float64{AccountEntity: {"Cost": {1}}}},
		[]string{"2026-01-02"}, 1)
	if err == nil {
		t.Fatal("an unknown model must fail")
	}
	if !strings.Contains(err.Error(), "nosuchmodel@270d") {
		t.Errorf("the error should name the window, got: %v", err)
	}
}
