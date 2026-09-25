package main

// One test per way this can go quietly wrong. No frameworks, no fixtures.
//
// Every test here maps to a real defect found by auditing the tool against its
// own data, not to a hypothetical.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// day returns the i'th consecutive day from 2026-01-01, so test data always has
// real dates. Hand-written "2026-01-32" is how the first draft of these tests
// accidentally proved the date validation works.
func day(i int) string {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
}

// goodCSV is a valid consecutive series long enough to be accepted, so each test
// can focus on the one thing it is checking.
func goodCSV(n int) string {
	var b strings.Builder
	b.WriteString("date,spend\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s,%d\n", day(i), 100+i)
	}
	return b.String()
}

func TestCSVRoundTrip(t *testing.T) {
	body := "date,spend\n" +
		"2026-01-01,100.5\n2026-01-02,\"1,200\"\n2026-01-03,£300\n"
	for i := 3; i < 40; i++ {
		body += fmt.Sprintf("%s,%d\n", day(i), i)
	}
	d, err := readCSV(writeTemp(t, "d.csv", body), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Names) != 1 || d.Names[0] != "spend" {
		t.Errorf("columns = %v, want [spend]", d.Names)
	}
	want := []Point{{"2026-01-01", 100.5}, {"2026-01-02", 1200}, {"2026-01-03", 300}}
	got := d.Series(AccountEntity, "spend")
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// Ad exports are commonly newest-first. Reading them in file order reverses the
// series and produces a confident, meaningless forecast.
func TestUnsortedCSVIsSortedNotTakenInFileOrder(t *testing.T) {
	var b strings.Builder
	b.WriteString("date,spend\n")
	for i := 39; i >= 0; i-- { // newest first, as ad exports often are
		fmt.Fprintf(&b, "%s,%d\n", day(i), i)
	}
	d, err := readCSV(writeTemp(t, "d.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(d.Days); i++ {
		if d.Days[i-1] >= d.Days[i] {
			t.Fatalf("not ascending at %d: %s then %s", i, d.Days[i-1], d.Days[i])
		}
	}
	if v := d.Values[AccountEntity]["spend"][0]; v != 0 {
		t.Errorf("first value = %v, want 0 (series was not reordered)", v)
	}
}

// Both models read the series as consecutive days, so a gap silently shifts
// every forecast date.
func TestDateGapIsRefused(t *testing.T) {
	body := goodCSV(40)
	body = strings.Replace(body, fmt.Sprintf("%s,%d\n", day(20), 120), "", 1)
	_, err := readCSV(writeTemp(t, "d.csv", body), nil, "")
	if err == nil {
		t.Fatal("a missing day must be refused")
	}
	if !strings.Contains(err.Error(), day(19)) || !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the gap, got: %v", err)
	}
}

func TestTooShortSeriesIsRefused(t *testing.T) {
	_, err := readCSV(writeTemp(t, "d.csv", goodCSV(5)), nil, "")
	if err == nil || !strings.Contains(err.Error(), "too few") {
		t.Fatalf("5 rows should be refused, got %v", err)
	}
}

func TestCSVBadRowsAreNamedNotSkipped(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"bad number", strings.Replace(goodCSV(40), day(2)+",102", day(2)+",oops", 1), "line 4"},
		{"bad date", strings.Replace(goodCSV(40), day(2)+",102", "not-a-date,102", 1), "line 4"},
		// A day that carries a different number of rows than the others means a
		// campaign is missing from it; adding those rows up invents a step.
		{"uneven rows per day", strings.Replace(goodCSV(40), day(2)+",102", day(1)+",102", 1),
			"has 2 rows but"},
		{"ragged row", strings.Replace(goodCSV(40), day(2)+",102", day(2)+",102,9", 1), "columns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readCSV(writeTemp(t, "d.csv", tc.body), nil, "")
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestCovariateColumnsAreRead(t *testing.T) {
	var b strings.Builder
	b.WriteString("date,spend,budget\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "%s,%d,%d\n", day(i), 100+i, 500)
	}
	d, err := readCSV(writeTemp(t, "d.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	col, ok := d.Values[AccountEntity]["budget"]
	if !ok {
		t.Fatalf("budget column missing, got %v", d.Names)
	}
	if len(col) != len(d.Days) {
		t.Errorf("budget has %d values, series has %d", len(col), len(d.Days))
	}
}

func TestDBRoundTripWithCovariates(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := &Data{
		Days:     []string{"2026-01-01", "2026-01-02"},
		Names:    []string{"spend", "budget"},
		Entities: []string{AccountEntity},
		Values: map[string]map[string][]float64{
			AccountEntity: {"spend": {1, 2}, "budget": {10, 20}},
		},
	}
	if err := saveData(db, "s", d); err != nil {
		t.Fatal(err)
	}
	if err := saveData(db, "s", d); err != nil { // re-saving must update, not duplicate
		t.Fatal(err)
	}
	got, err := loadSeries(db, "s", AccountEntity, "spend")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Value != 1 {
		t.Fatalf("target: got %v", got)
	}
	cov, err := loadSeries(db, "s", AccountEntity, "budget")
	if err != nil {
		t.Fatal(err)
	}
	if len(cov) != 2 || cov[1].Value != 20 {
		t.Fatalf("covariate: got %v", cov)
	}
}

func TestCheckForecastRejectsBadOutput(t *testing.T) {
	if _, err := checkForecast([][]float64{{1, 2, 3}, {1, 2, 3}}, 2, 3); err != nil {
		t.Fatalf("valid forecast rejected: %v", err)
	}
	nan := mathNaN()
	inf := mathInf()
	for _, tc := range []struct {
		name string
		q    [][]float64
	}{
		{"too few days", [][]float64{{1, 2, 3}}},
		{"wrong quantile count", [][]float64{{1, 2}, {1, 2}}},
		{"badly inverted quantiles", [][]float64{{300, 2, 1}, {1, 2, 3}}},
		{"NaN", [][]float64{{1, nan, 3}, {1, 2, 3}}},
		{"Inf", [][]float64{{1, inf, 3}, {1, 2, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := checkForecast(tc.q, 2, 3); err == nil {
				t.Error("expected rejection, got none")
			}
		})
	}
}

func TestParseFutureCountMustMatchHorizon(t *testing.T) {
	if _, err := parseFuture("budget=1,2,3", 7); err == nil {
		t.Error("3 values for a 7-day horizon should be rejected")
	}
	got, err := parseFuture("budget=1,2,3", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["budget"]) != 3 {
		t.Errorf("got %v", got)
	}
}

// Asking for one model and silently getting another is the worst failure this
// tool could have, so the argument reordering that prevents it is tested.
func TestFlagsAfterFilenameAreNotIgnored(t *testing.T) {
	got := reorderFlags([]string{"data.csv", "-model", "timesfm3", "-horizon", "7"})
	want := "-model timesfm3 -horizon 7 data.csv"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	if g := reorderFlags([]string{"-model=chronos2", "a.csv"}); strings.Join(g, " ") != "-model=chronos2 a.csv" {
		t.Errorf("equals form broken: %v", g)
	}
}

func TestNextDays(t *testing.T) {
	got, err := nextDays("2026-02-27", 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-02-28", "2026-03-01", "2026-03-02"} // 2026 is not a leap year
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("day %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func mathNaN() float64 { var z float64; return z / z }
func mathInf() float64 { var z float64; return 1 / z }

// --- Round 1 findings: real-world file quirks -------------------------------

func TestExcelBOMIsStripped(t *testing.T) {
	body := "\xef\xbb\xbf\"date\",\"spend\"\n" // UTF-8 BOM, as Excel writes it
	for i := 0; i < 40; i++ {
		body += fmt.Sprintf("%q,%q\n", day(i), fmt.Sprint(100+i))
	}
	d, err := readCSV(writeTemp(t, "bom.csv", body), nil, "")
	if err != nil {
		t.Fatalf("Excel's BOM must not break parsing: %v", err)
	}
	if len(d.Names) != 1 || d.Names[0] != "spend" {
		t.Errorf("columns = %v, want [spend] (BOM leaked into the header?)", d.Names)
	}
}

// 01/02/2026 is 1 February to most of the world and 2 January in the US. Picking
// silently would move every observation by up to eleven months.
func TestUndecidableDateOrderIsRefused(t *testing.T) {
	body := "date,v\n"
	for i := 1; i <= 12; i++ { // every component <= 12: nothing settles it
		body += fmt.Sprintf("%02d/01/2026,%d\n", i, 100+i)
	}
	_, err := readCSV(writeTemp(t, "amb.csv", body), nil, "")
	if err == nil {
		t.Fatal("ambiguous day/month order must be refused, not guessed")
	}
	if !strings.Contains(err.Error(), "day/month or month/day") {
		t.Errorf("error should explain the ambiguity, got: %v", err)
	}
}

func TestDateOrderResolvedByTheFile(t *testing.T) {
	for _, tc := range []struct{ name, layout string }{
		{"day first", "02/01/2006"},
		{"month first", "01/02/2006"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "date,v\n"
			for i := 0; i < 40; i++ { // spans a 31st, which settles the order
				d, _ := time.Parse("2006-01-02", day(i))
				body += fmt.Sprintf("%s,%d\n", d.Format(tc.layout), 100+i)
			}
			got, err := readCSV(writeTemp(t, "d.csv", body), nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.Days[0] != day(0) || got.Days[39] != day(39) {
				t.Errorf("read %s..%s, want %s..%s",
					got.Days[0], got.Days[39], day(0), day(39))
			}
		})
	}
}

// --- Round 1 finding: the validator was rejecting valid forecasts ------------

// The models compute in float32. On a smooth series adjacent quantiles collapse
// to the same value and cross by a few hundred ULPs; that is noise, not an
// inverted forecast. A fixed 1e-6 tolerance rejected real, correct output.
// Both models predict each quantile independently, so mild crossing is expected.
// It is corrected by sorting and reported, not treated as a failed forecast -- a
// real 0.17% crossing was observed on a 10,000-day series.
func TestSmallQuantileCrossingIsRepairedAndReported(t *testing.T) {
	q := [][]float64{{10061.11, 10044.47, 10100.0}} // q30 above q40, as observed
	worst, err := checkForecast(q, 1, 3)
	if err != nil {
		t.Fatalf("a normal crossing must not fail the forecast: %v", err)
	}
	if worst < 1e-4 {
		t.Errorf("crossing should be reported, got %v", worst)
	}
	for j := 1; j < len(q[0]); j++ {
		if q[0][j] < q[0][j-1] {
			t.Fatalf("quantiles not sorted after repair: %v", q[0])
		}
	}
}

func TestAbsurdCrossingIsStillRefused(t *testing.T) {
	// 50% out of order is not a model artefact, it is a broken forecast.
	if _, err := checkForecast([][]float64{{200, 100, 300}}, 1, 3); err == nil {
		t.Error("a 50% crossing must still be refused")
	}
}

// Sorting must not disturb a forecast that was already in order.
func TestCorrectForecastIsUnchanged(t *testing.T) {
	q := [][]float64{{1.5, 2.5, 3.5}, {10, 20, 30}}
	worst, err := checkForecast(q, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if worst != 0 {
		t.Errorf("no crossing expected, got %v", worst)
	}
	if q[0][0] != 1.5 || q[1][2] != 30 {
		t.Errorf("values changed: %v", q)
	}
}

// --- Round 3 finding: %.2f hid small values and printed negative zero --------

func TestFormatValue(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"}, // models return negative zero
		{1234.5678, "1234.57"},
		{12.34567, "12.346"},
		{0.000123456, "0.000123"},
	} {
		if got := formatValue(tc.in); got != tc.want {
			t.Errorf("formatValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := formatValue(6.00676e-08); !strings.Contains(got, "e-08") {
		t.Errorf("tiny values must not print as 0.00, got %q", got)
	}
}

// Naming a column twice silently kept the last one, discarding numbers the user typed.
func TestDuplicateFutureColumnIsRefused(t *testing.T) {
	if _, err := parseFuture("budget=1,2,3;budget=9,9,9", 3); err == nil {
		t.Fatal("naming the same column twice must be refused")
	}
	if _, err := parseFuture("budget=1,2,3;promo=0,0,1", 3); err != nil {
		t.Errorf("two different columns are fine: %v", err)
	}
}

// --- Real exports are wide and messy: "Day, Campaign, Impressions, Clicks, Cost"

func wideCSV(n int) string {
	var b strings.Builder
	b.WriteString("Day,Campaign,Impressions,Clicks,Cost\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s,Brand Search,%d,%d,%d.50\n", day(i), 40000+i, 600+i, 700+i)
	}
	return b.String()
}

// Taking column 2 on faith failed on the first real file anyone tried, because
// column 2 is usually a campaign name.
func TestTextColumnsAreSetAsideNotFatal(t *testing.T) {
	d, err := readCSV(writeTemp(t, "w.csv", wideCSV(40)), nil, "")
	if err != nil {
		t.Fatalf("a wide export must not fail: %v", err)
	}
	if len(d.Names) != 3 || d.Names[0] != "Impressions" {
		t.Errorf("numeric columns = %v, want [Impressions Clicks Cost]", d.Names)
	}
	if len(d.Skipped) != 1 || d.Skipped[0] != "Campaign" {
		t.Errorf("Campaign should be set aside, got %v", d.Skipped)
	}
	if _, ok := d.Values[AccountEntity]["Cost"]; !ok {
		t.Errorf("Cost should be available, got %v", d.Names)
	}
}

func TestColumnsFlagPicksASubset(t *testing.T) {
	d, err := readCSV(writeTemp(t, "w.csv", wideCSV(40)), []string{"Cost"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Names) != 1 || d.Names[0] != "Cost" {
		t.Errorf("columns = %v, want [Cost]", d.Names)
	}
}

func TestUnknownOrTextColumnIsRefusedWithOptions(t *testing.T) {
	_, err := readCSV(writeTemp(t, "w.csv", wideCSV(40)), []string{"Costs"}, "")
	if err == nil || !strings.Contains(err.Error(), "Impressions, Clicks, Cost") {
		t.Errorf("a typo should list the real columns, got: %v", err)
	}
	_, err = readCSV(writeTemp(t, "w.csv", wideCSV(40)), []string{"Campaign"}, "")
	if err == nil || !strings.Contains(err.Error(), "holds text, not numbers") {
		t.Errorf("a text column should say why it cannot be forecast, got: %v", err)
	}
}

// A single bad cell in an otherwise numeric column is a typo, and must still be
// reported with its line -- not quietly reclassified as a text column.
func TestOneBadCellStillNamesTheLine(t *testing.T) {
	body := strings.Replace(wideCSV(40), day(3)+",Brand Search,40003", day(3)+",Brand Search,oops", 1)
	_, err := readCSV(writeTemp(t, "w.csv", body), nil, "")
	if err == nil {
		t.Fatal("a bad cell must be an error")
	}
	if !strings.Contains(err.Error(), "line 5") {
		t.Errorf("error should name the line, got: %v", err)
	}
}

// --- Google Ads exports: one row per campaign per day ------------------------

// campaignCSV is the shape a Google Ads campaign report arrives in: every day
// repeated once per campaign, with text columns and an ID column alongside.
func campaignCSV(days, campaigns int, pausedFrom int) string {
	var b strings.Builder
	b.WriteString("Day,Campaign status,Campaign,Budget,Cost,Impr.,Clicks,Campaign ID\n")
	for i := 0; i < days; i++ {
		for c := 0; c < campaigns; c++ {
			status, cost, impr, clicks := "Enabled", 100+i+c*10, 5000+i*7, 200+i
			if c >= pausedFrom {
				status, cost, impr, clicks = "Paused", 0, 0, 0
			}
			fmt.Fprintf(&b, "%s,%s,Campaign %d,%d,%d,%d,%d,%d\n",
				day(i), status, c, 50+c*10, cost, impr, clicks, 2000000000+c)
		}
	}
	return b.String()
}

func TestCampaignExportSplitsByCampaign(t *testing.T) {
	d, err := readCSV(writeTemp(t, "ads.csv", campaignCSV(40, 3, 3)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.RowsPerDay != 3 {
		t.Errorf("rows per day = %d, want 3", d.RowsPerDay)
	}
	if d.GroupBy != "Campaign" {
		t.Errorf("grouped by %q, want Campaign", d.GroupBy)
	}
	if len(d.Days) != 40 {
		t.Errorf("got %d days, want 40 (rows must collapse to days)", len(d.Days))
	}
	if len(d.Entities) != 4 { // account + 3 campaigns
		t.Errorf("entities = %v, want the account plus 3 campaigns", d.Entities)
	}
	if d.Entities[0] != AccountEntity {
		t.Errorf("first entity = %q, want %q", d.Entities[0], AccountEntity)
	}
	if len(d.Raw) != 120 {
		t.Errorf("kept %d raw rows, want 120", len(d.Raw))
	}
}

// Campaign ID is numeric, but adding fifteen of them together is meaningless.
func TestIdentifierColumnIsNotForecast(t *testing.T) {
	d, err := readCSV(writeTemp(t, "ads.csv", campaignCSV(40, 3, 3)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Names {
		if n == "Campaign ID" {
			t.Fatal("Campaign ID must not be forecast")
		}
	}
	if len(d.Identifiers) != 1 || d.Identifiers[0] != "Campaign ID" {
		t.Errorf("identifiers = %v, want [Campaign ID]", d.Identifiers)
	}
}

// The account total must be the sum of the campaigns, day by day.
func TestAccountIsTheSumOfCampaigns(t *testing.T) {
	d, err := readCSV(writeTemp(t, "ads.csv", campaignCSV(40, 3, 3)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range d.Names {
		for i := range d.Days {
			sum := 0.0
			for _, e := range d.Entities {
				if e != AccountEntity {
					sum += d.Values[e][metric][i]
				}
			}
			if got := d.Values[AccountEntity][metric][i]; math.Abs(got-sum) > 1e-9 {
				t.Fatalf("%s on %s: account %v, campaigns sum to %v",
					metric, d.Days[i], got, sum)
			}
		}
	}
}

// A paused campaign never changes, so there is nothing to forecast. It must be
// named, kept in the data, and left out of the model calls.
func TestUnchangingCampaignsAreNotForecast(t *testing.T) {
	d, err := readCSV(writeTemp(t, "ads.csv", campaignCSV(40, 4, 2)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Inactive) != 2 {
		t.Errorf("inactive = %v, want the 2 paused campaigns", d.Inactive)
	}
	for _, e := range d.Entities {
		for _, dead := range d.Inactive {
			if e == dead {
				t.Errorf("%q has no activity and must not be forecast", e)
			}
		}
	}
	// still stored, just not forecast
	for _, dead := range d.Inactive {
		if _, ok := d.Values[dead]; !ok {
			t.Errorf("%q was dropped; it should still be stored", dead)
		}
	}
}

// A campaign that spent heavily for a year and was then switched off is not a
// forecasting question: it will spend nothing until someone turns it back on.
// The old rule only caught series that never moved, so a campaign like this was
// sent to the models, which returned noise around zero whose quantiles came back
// out of order and took a whole report down with it.
//
// It must still be stored in full. Only the model calls skip it.
func TestSwitchedOffCampaignsAreNotForecast(t *testing.T) {
	var b strings.Builder
	// A serving-status column sits next to the campaign-status one in a real
	// export, and must not be mistaken for it.
	b.WriteString("Day,Campaign status,Campaign,Status,Cost,Impr.,Clicks\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Enabled,Live,Eligible (Limited),%d,%d,%d\n",
			day(i), 100+i, 5000+i*7, 20+i)
		fmt.Fprintf(&b, "%s,Enabled,Live Two,Eligible (Limited),%d,%d,%d\n",
			day(i), 60+i, 3000+i*5, 10+i)
		// Spent for the first 60 days, switched off after that.
		status, cost := "Enabled", 80+i
		if i >= 60 {
			status, cost = "Paused", 0
		}
		fmt.Fprintf(&b, "%s,%s,Zombie,Eligible,%d,%d,%d\n", day(i), status, cost, cost*40, cost/4)
	}
	d, err := readCSV(writeTemp(t, "ads.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"Zombie"}; len(d.Paused) != 1 || d.Paused[0] != want[0] {
		t.Fatalf("paused = %v, want %v", d.Paused, want)
	}
	for _, e := range d.Entities {
		if e == "Zombie" {
			t.Error("Zombie is switched off and must not be sent to a model")
		}
	}
	if !slicesContainsFold(d.Entities, "Live") {
		t.Errorf("Live is switched on and must be forecast; entities = %v", d.Entities)
	}
	if !slicesContainsFold(d.Inactive, "Zombie") {
		t.Errorf("Zombie must be named as excluded; inactive = %v", d.Inactive)
	}

	// Every campaign is still stored, in full, whatever its status.
	col, ok := d.Values["Zombie"]["Cost"]
	if !ok {
		t.Fatal("Zombie was dropped from the data; it must still be stored")
	}
	if len(col) != len(d.Days) {
		t.Errorf("Zombie has %d days stored, want all %d", len(col), len(d.Days))
	}
	if col[0] == 0 {
		t.Error("Zombie's spending history was not kept")
	}
	// The account total still includes it -- that is what the account spent.
	if d.Values[AccountEntity]["Cost"][0] != d.Values["Live"]["Cost"][0]+
		d.Values["Live Two"]["Cost"][0]+col[0] {
		t.Error("the account total must still include switched-off campaigns")
	}
}

// The status column is found by its values, so a file without one still works.
func TestNoStatusColumnFallsBackToTheNeverMovedRule(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Impr.,Clicks\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Live,%d,%d,%d\n", day(i), 100+i, 5000+i*7, 20+i)
		fmt.Fprintf(&b, "%s,Flat,0,0,0\n", day(i))
	}
	d, err := readCSV(writeTemp(t, "ads.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Paused) != 0 {
		t.Errorf("no status column, so nothing can be known to be paused: %v", d.Paused)
	}
	if len(d.Inactive) != 1 || d.Inactive[0] != "Flat" {
		t.Errorf("inactive = %v, want [Flat]", d.Inactive)
	}
}

// The Go and Python sides have to agree on what "switched on" means. The Python
// half is checked by its own stdlib self-check; run it here so it cannot rot.
func TestFinetuneAgreesOnWhatIsRunning(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH")
	}
	out, err := exec.Command(python, filepath.Join("models", "test_finetune.py")).CombinedOutput()
	if err != nil {
		t.Fatalf("models/test_finetune.py failed: %v\n%s", err, out)
	}
}

// A rate is a real series and is forecast like any other. What it is not is
// addable: fifteen campaigns' click-through rates do not sum to the account's,
// so the account figure is their mean.
func TestRatioColumnsAreForecastButAveragedNotSummed(t *testing.T) {
	body := strings.Replace(campaignCSV(40, 3, 3), "Clicks,Campaign ID", "Clicks,CTR,Campaign ID", 1)
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i := 1; i < len(lines); i++ {
		p := strings.Split(lines[i], ",")
		lines[i] = strings.Join(append(p[:7:7], "4.00%", p[7]), ",")
	}
	d, err := readCSV(writeTemp(t, "ads.csv", strings.Join(lines, "\n")+"\n"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContainsFold(d.Names, "CTR") {
		t.Fatalf("CTR must be forecast, got %v", d.Names)
	}
	if !d.Percent["CTR"] {
		t.Error("CTR was written with a %% sign and should be marked as a percentage")
	}
	// three campaigns all at 4.00%: the account is 4.00%, not 12.00%
	if got := d.Values[AccountEntity]["CTR"][0]; math.Abs(got-4.0) > 1e-9 {
		t.Errorf("account CTR = %v, want 4 (the mean), not 12 (the sum)", got)
	}
	if !slicesContainsFold(d.Averaged, "CTR") {
		t.Errorf("CTR should be listed as averaged, got %v", d.Averaged)
	}
}

// A percentage is a number. Reading "4.20%" as text dropped the column entirely.
func TestPercentagesAreNumbers(t *testing.T) {
	var b strings.Builder
	b.WriteString("date,Cost,CTR\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "%s,%d,%.2f%%\n", day(i), 100+i, 4.2+float64(i)*0.01)
	}
	d, err := readCSV(writeTemp(t, "p.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContainsFold(d.Names, "CTR") {
		t.Fatalf("CTR must be a forecastable number, got names=%v skipped=%v", d.Names, d.Skipped)
	}
	// kept as written, so the report can put the sign back unchanged
	if got := d.Values[AccountEntity]["CTR"][0]; math.Abs(got-4.2) > 1e-9 {
		t.Errorf("CTR[0] = %v, want 4.2 as written", got)
	}
	if !d.Percent["CTR"] {
		t.Error("CTR should be marked as a percentage")
	}
	if formatMetric(4.2, true) != "4.200%" {
		t.Errorf("formatMetric = %q, want the sign back", formatMetric(4.2, true))
	}
}

// A budget is a dial you turn, not an outcome you measure. Forecasting it just
// replays the number you set -- seven days of 5877.00 on the real file.
func TestSettingsAreStoredButNotForecast(t *testing.T) {
	d, err := readCSV(writeTemp(t, "ads.csv", campaignCSV(40, 3, 3)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if slicesContainsFold(d.Names, "Budget") {
		t.Error("Budget must not be forecast")
	}
	if !slicesContainsFold(d.Settings, "Budget") {
		t.Errorf("Budget should be listed as a setting, got %v", d.Settings)
	}
	// still kept, for the record
	if _, ok := d.Values[AccountEntity]["Budget"]; !ok {
		t.Error("Budget must still be stored")
	}
	if got := len(d.Values[AccountEntity]["Budget"]); got != len(d.Days) {
		t.Errorf("Budget has %d values, want %d", got, len(d.Days))
	}
}

// Cost per acquisition is cost divided by conversions: an outcome, not a dial.
func TestCPAIsNotTreatedAsASetting(t *testing.T) {
	for _, name := range []string{"CPA", "Cost / conv.", "New customer CPA"} {
		if looksLikeSetting(name) {
			t.Errorf("%q is an outcome and must be forecast, not treated as a setting", name)
		}
	}
	for _, name := range []string{"Budget", "Daily budget", "Target CPA", "Max CPC bid"} {
		if !looksLikeSetting(name) {
			t.Errorf("%q is something you set and must not be forecast", name)
		}
	}
}

// A single-row-per-day file must behave exactly as before.
func TestPlainDailyFileStillHasOneEntity(t *testing.T) {
	d, err := readCSV(writeTemp(t, "d.csv", goodCSV(40)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.RowsPerDay != 1 || d.GroupBy != "" {
		t.Errorf("rows/day=%d groupBy=%q, want 1 and empty", d.RowsPerDay, d.GroupBy)
	}
	if len(d.Entities) != 1 || d.Entities[0] != AccountEntity {
		t.Errorf("entities = %v, want just the account", d.Entities)
	}
}

// A campaign actually called "(account)" would be folded into the total and
// disappear. Refused rather than quietly lost.
func TestCampaignNamedLikeTheAccountIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "%s,%s,%d\n%s,Brand,%d\n", day(i), AccountEntity, 100+i, day(i), 50+i)
	}
	_, err := readCSV(writeTemp(t, "c.csv", b.String()), nil, "")
	if err == nil {
		t.Fatal("a campaign named like the account total must be refused")
	}
	if !strings.Contains(err.Error(), AccountEntity) {
		t.Errorf("the error should name the clash, got: %v", err)
	}
}

// Campaign names routinely contain commas -- "Video Efficient Reach, Infeed,
// 9-16-25, CPM" is real -- so -entities is separated by semicolons.
func TestEntitySelectionSurvivesCommasInNames(t *testing.T) {
	got := splitOn("Video Efficient Reach, Infeed, 9-16-25, CPM; Brand", ";")
	if len(got) != 2 {
		t.Fatalf("got %d entities, want 2: %q", len(got), got)
	}
	if got[0] != "Video Efficient Reach, Infeed, 9-16-25, CPM" {
		t.Errorf("first entity = %q, the commas should have survived", got[0])
	}
	if got[1] != "Brand" {
		t.Errorf("second entity = %q", got[1])
	}
	// -columns and -future still split on commas
	if len(splitList("a,b,c")) != 3 {
		t.Error("comma splitting is still needed for -columns")
	}
}

// Every column in a real Google Ads export must land in the right bucket.
// "Max CPC bid" ends in "id" and was read as an identifier, which stopped the
// settings rule from ever seeing it.
func TestColumnClassification(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Cost", "forecast"}, {"Impr.", "forecast"}, {"Clicks", "forecast"},
		{"Conversions", "forecast"}, {"Conv. value", "forecast"}, {"Revenue", "forecast"},
		{"CPA", "forecast"}, {"Cost / conv.", "forecast"}, {"New customer CPA", "forecast"},

		{"Budget", "setting"}, {"Daily budget", "setting"}, {"Target CPA", "setting"},
		{"Target ROAS", "setting"}, {"Max CPC bid", "setting"},

		{"Campaign ID", "identifier"}, {"Customer ID", "identifier"},
		{"Ad group ID", "identifier"}, {"Currency code", "identifier"},

		{"CTR", "rate"}, {"Conv. rate", "rate"}, {"Avg. CPC", "rate"},
		{"Search impr. share", "rate"},
	} {
		got := "forecast"
		switch {
		case looksLikeIdentifier(tc.name):
			got = "identifier"
		case looksLikeSetting(tc.name):
			got = "setting"
		case looksLikeRatio(tc.name):
			got = "rate"
		}
		if got != tc.want {
			t.Errorf("%-20q -> %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Three models, and adding the third required exactly one line of Go.
func TestThreeModelsRegistered(t *testing.T) {
	for _, want := range []string{"timesfm3", "chronos2", "chronos2ft"} {
		if _, ok := models[want]; !ok {
			t.Errorf("model %q is not registered", want)
		}
	}
	if len(models) != 3 {
		t.Errorf("registered models = %v, want exactly 3", modelNames())
	}
	// worker file names must not shadow a Python package
	for name, path := range models {
		if !strings.HasSuffix(path, "_worker.py") {
			t.Errorf("%s -> %s: workers must end in _worker.py so they cannot shadow "+
				"an installed package", name, path)
		}
	}
}

// A budget is a setting, so it is never forecast -- and it is also the textbook
// known-future value, because you chose next week's yourself. Classifying it as
// a setting must not stop -future accepting it.
func TestSettingsCanBeUsedAsKnownFutureInputs(t *testing.T) {
	var b strings.Builder
	b.WriteString("date,spend,Budget\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "%s,%d,%d\n", day(i), 400+i, 500)
	}
	d, err := readCSV(writeTemp(t, "b.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if slicesContainsFold(d.Names, "Budget") {
		t.Fatal("Budget should not be forecastable")
	}
	// but it must be present as a stored numeric column, which is what -future
	// validates against
	if _, ok := d.Values[AccountEntity]["Budget"]; !ok {
		t.Error("Budget must still be available as a known-future input")
	}
	if len(d.Values[AccountEntity]["Budget"]) != len(d.Days) {
		t.Error("Budget history must be full length for use as a covariate")
	}
}

// A fine-tuned adapter is registered by a path relative to models/, so moving
// the project does not break it -- and so it is never shipped with an absolute
// path baked in.
func TestFinetunedAdapterPathIsRelative(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("models", "finetuned.json"))
	if err != nil {
		t.Skip("no fine-tuned model registered")
	}
	var reg map[string]struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	for name, m := range reg {
		if filepath.IsAbs(m.Path) {
			t.Errorf("%s path %q is absolute; it will break when the project moves",
				name, m.Path)
		}
	}
}

// The adapter is fitted to one person's numbers and registered on their machine.
// Sending it to someone else gives them a broken or wrong model.
func TestShareScriptExcludesTheAdapter(t *testing.T) {
	b, err := os.ReadFile("share.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"models/finetuned", "models/finetuned.json"} {
		if !strings.Contains(s, "--exclude='"+want+"'") {
			t.Errorf("share.sh must exclude %s", want)
		}
	}
	// but the means to train one must travel
	if strings.Contains(s, "--exclude='models/finetune.py'") {
		t.Error("finetune.py must be shared so the recipient can train their own")
	}
}

// Counting rows per day is not enough: a day that lists one campaign twice and
// another not at all has the right number of rows. Before this was caught, the
// duplicate was added to itself and the absent campaign was stored as a real
// zero -- 1119 against a true 120, and a fabricated step in the other series.
// The account total stayed right, which is why nothing looked wrong.
func TestADuplicatedRowCannotHideAMissingCampaign(t *testing.T) {
	build := func(mutate func(i int) []string) string {
		var b strings.Builder
		b.WriteString("Day,Campaign,Cost\n")
		for i := 0; i < 120; i++ {
			for _, line := range mutate(i) {
				fmt.Fprintf(&b, "%s,%s\n", day(i), line)
			}
		}
		return b.String()
	}
	both := func(int) []string { return []string{"Brand,120", "Shopping,500"} }

	// The shape that used to pass silently.
	dup := build(func(i int) []string {
		if i == 60 {
			return []string{"Brand,120", "Brand,999"}
		}
		return both(i)
	})
	_, err := readCSV(writeTemp(t, "dup.csv", dup), nil, "")
	if err == nil {
		t.Fatal("a day listing one campaign twice must be refused")
	}
	for _, want := range []string{day(60), "twice", "Brand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}

	// The same defect with three campaigns, where the duplicate hides a different
	// one. A brand-new name on one day cannot arise on its own: it changes the
	// file's distinct count and findGroupColumn refuses the file first.
	var t3 strings.Builder
	t3.WriteString("Day,Campaign,Cost\n")
	for i := 0; i < 120; i++ {
		rows := []string{"A,100", "B,200", "C,300"}
		if i == 60 {
			rows = []string{"A,100", "B,200", "B,900"} // C missing, B doubled
		}
		for _, r := range rows {
			fmt.Fprintf(&t3, "%s,%s\n", day(i), r)
		}
	}
	if _, err := readCSV(writeTemp(t, "three.csv", t3.String()), nil, ""); err == nil {
		t.Error("a duplicate hiding a third campaign must be refused")
	} else if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error should say a campaign appears twice, got: %v", err)
	}

	// And a consistent file must still be read.
	d, err := readCSV(writeTemp(t, "ok.csv", build(both)), nil, "")
	if err != nil {
		t.Fatalf("a consistent file must still import: %v", err)
	}
	if got := d.Values["Brand"]["Cost"][60]; got != 120 {
		t.Errorf("Brand on the middle day = %v, want 120", got)
	}
}

// -fill-absent must be a no-op on an export that already lists every campaign on
// every day. Most exports are dense, so the flag has to be safe to leave on:
// if it changed a dense file at all it would be a trap rather than a repair.
func TestFillAbsentChangesNothingOnADenseExport(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign status,Campaign,Cost,Impr.,Clicks\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Enabled,Live,%d,%d,%d\n", day(i), 100+i, 5000+i*7, 20+i)
		fmt.Fprintf(&b, "%s,Enabled,Live Two,%d,%d,%d\n", day(i), 60+i, 3000+i*5, 10+i)
	}
	path := writeTemp(t, "dense.csv", b.String())

	plain, err := readCSV(path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	filled, err := readCSVFilling(path, nil, "", true)
	if err != nil {
		t.Fatal(err)
	}

	if len(filled.Stopped) != 0 {
		t.Errorf("nothing stopped in a dense export; stopped = %v", filled.Stopped)
	}
	if len(filled.Raw) != len(plain.Raw) {
		t.Errorf("raw rows = %d with the flag, %d without", len(filled.Raw), len(plain.Raw))
	}
	if !reflect.DeepEqual(plain.Entities, filled.Entities) {
		t.Errorf("entities = %v with the flag, %v without", filled.Entities, plain.Entities)
	}
	if !reflect.DeepEqual(plain.Values, filled.Values) {
		t.Error("the numbers differ with the flag on; on a dense export they must not")
	}
}

// The reason the flag exists, and the reason it is not enough on its own: a
// campaign the export stops listing has stopped running, so the zeros filled in
// after it are not a forecasting question. Forecasting that tail returns noise
// around zero whose quantiles come back out of order and fails the whole run --
// which is what happened before this rule existed.
func TestFillAbsentDoesNotForecastACampaignTheExportStoppedListing(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Impr.,Clicks\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Live,%d,%d,%d\n", day(i), 100+i, 5000+i*7, 20+i)
		if i < 40 { // ran for 40 days, then the export stops mentioning it
			fmt.Fprintf(&b, "%s,Gone,%d,%d,%d\n", day(i), 80+i, 4000+i*3, 15+i)
		}
		if i >= 30 { // started late and is still running on the last day
			fmt.Fprintf(&b, "%s,New,%d,%d,%d\n", day(i), 50+i, 2000+i*2, 8+i)
		}
	}
	path := writeTemp(t, "ragged.csv", b.String())

	if _, err := readCSV(path, nil, ""); err == nil {
		t.Fatal("a ragged export must be refused without the flag")
	}

	d, err := readCSVFilling(path, nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Gone"}; !reflect.DeepEqual(d.Stopped, want) {
		t.Fatalf("stopped = %v, want %v", d.Stopped, want)
	}
	if slicesContainsFold(d.Entities, "Gone") {
		t.Error("Gone stopped running and must not be sent to a model")
	}
	if !slicesContainsFold(d.Entities, "New") {
		t.Error("New started late but is still running, so it must be forecast")
	}

	// Stored in full all the same, zeros and history alike.
	col, ok := d.Values["Gone"]["Cost"]
	if !ok {
		t.Fatal("Gone was dropped from the data; it must still be stored")
	}
	if len(col) != len(d.Days) {
		t.Errorf("Gone has %d days stored, want all %d", len(col), len(d.Days))
	}
	if col[0] == 0 || col[len(col)-1] != 0 {
		t.Error("Gone must keep its real history and be zero after it stopped")
	}
	// The filled zeros are not rows the platform sent, so they stay out of raw.
	for _, r := range d.Raw {
		if r.Data["Campaign"] == "Gone" && r.Day > day(39) {
			t.Errorf("a synthesised row for %s reached the raw table", r.Day)
		}
	}
}

// A day missing from the middle of a campaign's own run is a broken export, not
// a campaign that was not running. Filling it with zeros would invent a day the
// campaign did spend on, so it is refused instead -- the flag repairs a shape,
// it does not paper over a bad download.
func TestFillAbsentRefusesAHoleInsideARun(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Impr.,Clicks\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Live,%d,%d,%d\n", day(i), 100+i, 5000+i*7, 20+i)
		if i != 60 { // ran throughout, but day 60 is missing from the file
			fmt.Fprintf(&b, "%s,Holed,%d,%d,%d\n", day(i), 80+i, 4000+i*3, 15+i)
		}
	}
	_, err := readCSVFilling(writeTemp(t, "holed.csv", b.String()), nil, "", true)
	if err == nil {
		t.Fatal("a gap inside a run must be refused, not filled")
	}
	for _, want := range []string{"Holed", "inside", "Re-export"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// -by names the label column. The fill has to honour it, or it would group the
// zero rows by one column while the forecast splits campaigns by another.
func TestFillAbsentHonoursTheNamedColumn(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign ID,Campaign,Cost,Impr.,Clicks\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,11,Live,%d,%d,%d\n", day(i), 100+i, 5000+i*7, 20+i)
		if i < 40 {
			fmt.Fprintf(&b, "%s,22,Gone,%d,%d,%d\n", day(i), 80+i, 4000+i*3, 15+i)
		}
	}
	path := writeTemp(t, "ids.csv", b.String())

	d, err := readCSVFilling(path, nil, "Campaign ID", true)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroupBy != "Campaign ID" {
		t.Fatalf("grouped by %q, want %q", d.GroupBy, "Campaign ID")
	}
	if want := []string{"22"}; !reflect.DeepEqual(d.Stopped, want) {
		t.Errorf("stopped = %v, want %v", d.Stopped, want)
	}

	// A column that is forecast can never be the label, flag or no flag.
	if _, err := readCSVFilling(path, nil, "Cost", true); err == nil {
		t.Error("-by Cost must still be refused: a measurement is not a label")
	}
}

// No data is 0. A blank, a dash or a NaN is a cell the platform had no value for
// -- usually a day with no spend, sometimes a day with spend and no conversions,
// sometimes the reverse. All three mean nothing happened.
//
// Reading them as errors cost a real import four of its metrics: 11 blank cells
// in 1,392 rows made "Unique link clicks" a text column, and a demoted column
// just stops appearing in the forecast. A stray word must still be an error,
// though, or a typo or the wrong file passes silently.
func TestNoDataParsesAsZero(t *testing.T) {
	for _, s := range []string{"", " ", "-", "--", "---", "–", "—",
		"N/A", "n/a", "NA", "NaN", "nan", "null", "NULL", "none", "nil"} {
		v, _, err := parseCell(s)
		if err != nil {
			t.Errorf("parseCell(%q) = %v, want 0 with no error", s, err)
		}
		if v != 0 {
			t.Errorf("parseCell(%q) = %v, want 0", s, v)
		}
	}
	// "$" is not in the list: the currency symbol is decoration, so a cell holding
	// only one is an empty cell, which is 0 like any other.
	for _, s := range []string{"abc", "12abc", "Enabled", "1.2.3", "Inf",
		"Infinity", "-Inf"} {
		if _, _, err := parseCell(s); err == nil {
			t.Errorf("parseCell(%q) must stay an error: a stray word is a typo, "+
				"and infinity is a division that went wrong, not a missing number", s)
		}
	}
}

// A metric column with a few blank cells stays a metric column, so it is still
// forecast. This is the whole point of the rule above.
func TestBlankCellsDoNotDemoteAMetricColumn(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Clicks,Purchases,Indicator\n")
	for i := 0; i < 120; i++ {
		clicks, purch := fmt.Sprint(20+i), fmt.Sprint(i%7)
		if i%37 == 0 { // the platform had nothing to report for these days
			clicks, purch = "", "-"
		}
		fmt.Fprintf(&b, "%s,Live,%d,%s,%s,purchase\n", day(i), 100+i, clicks, purch)
		fmt.Fprintf(&b, "%s,Live Two,%d,%d,%d,purchase\n", day(i), 60+i, 10+i, i%5)
	}
	d, err := readCSV(writeTemp(t, "blanks.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cost", "Clicks", "Purchases"} {
		if !slicesContainsFold(d.Names, want) {
			t.Errorf("%q was demoted out of the forecast; names = %v", want, d.Names)
		}
	}
	if slicesContainsFold(d.Names, "Indicator") {
		t.Error("a column of words is still text, blanks or not")
	}
	if got := d.Values["Live"]["Clicks"][0]; got != 0 {
		t.Errorf("a blank cell read as %v, want 0", got)
	}
}

// ingest.go's noData and finetune.py's NO_DATA are the same set, and have to be.
// The trainer and the forecaster reading a blank differently means the adapter is
// fitted to numbers the forecast never sees -- which is how num() and parseCell
// drifted apart once before, in both directions.
func TestNoDataSetsAgree(t *testing.T) {
	pick := func(path, re string) []string {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(re).FindStringSubmatch(string(src))
		if m == nil {
			t.Fatalf("%s: could not find the no-data set", path)
		}
		var out []string
		for _, q := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(m[1], -1) {
			s, err := strconv.Unquote(`"` + q[1] + `"`)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		sort.Strings(out)
		return out
	}
	goSet := pick("ingest.go", `(?s)var noData = map\[string\]bool\{(.*?)\n\}`)
	pySet := pick("models/finetune.py", `(?s)NO_DATA = \{(.*?)\}`)
	if len(goSet) == 0 {
		t.Fatal("no entries parsed out of ingest.go")
	}
	if !reflect.DeepEqual(goSet, pySet) {
		t.Errorf("noData and NO_DATA have drifted:\n  ingest.go:   %q\n  finetune.py: %q",
			goSet, pySet)
	}
}

// The allow-list decides what is forecast, and the order it is tested in is
// load-bearing. "Cost per conversion" contains both "cost" and "conversion", so
// whichever concept is tested first wins -- and if that were "spend", a per-unit
// cost would be summed across campaigns as though it were money spent.
func TestMetricConceptOrderIsLoadBearing(t *testing.T) {
	for _, c := range []struct {
		column  string
		concept string
		addable bool
	}{
		{"Cost", "spend", true},
		{"Amount spent (USD)", "spend", true},
		{"Spend", "spend", true},
		{"Impr.", "impressions", true},
		{"Impressions", "impressions", true},
		{"Video views", "impressions", true},
		{"Clicks", "clicks", true},
		{"Unique link clicks", "clicks", true},
		{"Purchases", "conversions", true},
		{"Website purchases", "conversions", true},
		{"Results", "conversions", true},
		{"Revenue", "revenue", true},
		{"Conversion value", "revenue", true}, // revenue, not a conversion count
		{"Cost per conversion", "cost per", false},
		{"Cost per add to cart (USD)", "cost per", false},
		{"CPA", "cost per", false},
		{"CPC", "cost per", false},
		{"Conversion rate", "rate", false}, // rate, not a conversion count
		{"CTR", "rate", false},
		{"ROAS", "rate", false},
	} {
		got, addable, ok := metricConcept(c.column)
		if !ok {
			t.Errorf("%q matched nothing; it is a metric we forecast", c.column)
			continue
		}
		if got != c.concept {
			t.Errorf("%q = %q, want %q", c.column, got, c.concept)
		}
		if addable != c.addable {
			t.Errorf("%q addable = %v, want %v -- a per-unit cost or rate does not "+
				"sum across campaigns", c.column, addable, c.addable)
		}
	}

	// Patterns match whole words, never substrings. A substring rule for "imp"
	// would claim "Impact", and one for "order" would claim "Reorder rank".
	for _, col := range []string{"Impact", "Quality score", "Ad relevance",
		"Days since launch", "Frequency cap reached"} {
		if c, _, ok := metricConcept(col); ok {
			t.Errorf("%q matched %q; it is not one of the metrics we forecast",
				col, c)
		}
	}
}

// A numeric column nobody asked for is stored and named, never forecast. This is
// the whole point: an export sends the columns the platform wants to send, and
// failing open meant each new one had to be excluded by name after it leaked.
func TestUnwantedNumericColumnsAreNamedNotForecast(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Clicks,Quality score,Days since launch\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Live,%d,%d,%d,%d\n", day(i), 100+i, 20+i, 7, i)
		fmt.Fprintf(&b, "%s,Live Two,%d,%d,%d,%d\n", day(i), 60+i, 10+i, 5, i)
	}
	d, err := readCSV(writeTemp(t, "extra.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Unfiltered {
		t.Error("Cost and Clicks matched, so the allow-list must be in force")
	}
	for _, want := range []string{"Cost", "Clicks"} {
		if !slicesContainsFold(d.Names, want) {
			t.Errorf("%q must be forecast; names = %v", want, d.Names)
		}
	}
	for _, unwanted := range []string{"Quality score", "Days since launch"} {
		if slicesContainsFold(d.Names, unwanted) {
			t.Errorf("%q must not be forecast", unwanted)
		}
		if !slicesContainsFold(d.NotMetrics, unwanted) {
			t.Errorf("%q must be named as set aside, not dropped in silence; "+
				"notMetrics = %v", unwanted, d.NotMetrics)
		}
	}
}

// A plain two-column series names its metric whatever the person liked. Filtering
// there would refuse the simplest possible input and buy nothing, so when nothing
// matches the allow-list it is not applied at all.
func TestAllowListDoesNotApplyWhenNothingMatches(t *testing.T) {
	var b strings.Builder
	b.WriteString("date,ramp,flat,wave\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,%d,50,%d\n", day(i), i, 40+i%7)
	}
	d, err := readCSV(writeTemp(t, "plain.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Unfiltered {
		t.Error("nothing matched, so the allow-list must not have been applied")
	}
	if len(d.NotMetrics) != 0 {
		t.Errorf("nothing may be set aside here; notMetrics = %v", d.NotMetrics)
	}
	if !slicesContainsFold(d.Names, "ramp") || !slicesContainsFold(d.Names, "wave") {
		t.Errorf("every numeric column must still be forecast; names = %v", d.Names)
	}

	// -columns is the person saying which metrics they want, and overrides it.
	d2, err := readCSV(writeTemp(t, "plain2.csv", b.String()), []string{"ramp"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !d2.Unfiltered || len(d2.NotMetrics) != 0 {
		t.Errorf("-columns must override the allow-list; unfiltered=%v notMetrics=%v",
			d2.Unfiltered, d2.NotMetrics)
	}
}

// Fifteen campaigns' spend adds up to the account's. Fifteen campaigns' cost per
// purchase does not. This was wrong on a real export: looksLikeRatio knew only
// "ctr", "rate", "%", "ratio", "share" and "avg", so "Cost per results" was summed
// and the account figure was not a number that meant anything.
func TestPerUnitCostIsAveragedNotSummed(t *testing.T) {
	var b strings.Builder
	b.WriteString("Day,Campaign,Cost,Cost per purchase\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%s,Live,%d,10\n", day(i), 100+i)
		fmt.Fprintf(&b, "%s,Live Two,%d,20\n", day(i), 60+i)
	}
	d, err := readCSV(writeTemp(t, "cpa.csv", b.String()), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContainsFold(d.Averaged, "Cost per purchase") {
		t.Fatalf("a per-unit cost must be averaged; averaged = %v", d.Averaged)
	}
	if got := d.Values[AccountEntity]["Cost per purchase"][0]; got != 15 {
		t.Errorf("account cost per purchase = %v, want 15 (the mean of 10 and 20, "+
			"not the sum)", got)
	}
	// Money still adds up.
	if got := d.Values[AccountEntity]["Cost"][0]; got != 160 {
		t.Errorf("account Cost = %v, want 160 (100+60)", got)
	}
}
