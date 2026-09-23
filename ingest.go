package main

// Reading a CSV of dates and numbers.
//
// Column 1 is the date. Column 2 is the thing being forecast. Any further
// columns are covariates -- things that move alongside it, like budget -- named
// by the header row.
//
// Deliberately strict. Anything that cannot be read, or that would quietly
// change the meaning of the series, stops the import and is named. The failures
// this guards against all look identical to success in the output:
//
//   - a row silently skipped leaves a hole the model reads as a real dip
//   - rows in the wrong order (ad exports are often newest-first) reverse the series
//   - a missing day treated as continuous shifts every forecast date

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// smallestUsefulSeries is one input patch for the larger of the two models
// (TimesFM 3.0 patches by 32, Chronos-2 by 16). Below this there is not enough
// for either to see a pattern, and a confident-looking forecast from three
// numbers is worse than a refusal.
const smallestUsefulSeries = 32

// Data is one CSV: the days, every column that holds numbers, and the names of
// the ones that do not.
//
// Which columns get forecast is decided later, in cmdForecast -- both models are
// multivariate, so the normal case is all of them at once.
// AccountEntity is the name used for the total across every campaign.
// Parenthesised so it cannot collide with a real campaign name.
const AccountEntity = "(account)"

type Data struct {
	Days  []string // ascending, one per day
	Names []string // numeric columns eligible to forecast, in file order

	// Entities are the things being forecast separately: AccountEntity first,
	// then one per campaign. A file with one row per day has only AccountEntity.
	Entities []string
	// GroupBy is the column the entities came from ("Campaign"), empty when the
	// file has a single row per day.
	GroupBy string
	// Values is entity -> metric -> one value per day.
	Values map[string]map[string][]float64
	// Inactive lists entities whose every metric is constant for the whole period
	// (paused campaigns). Nothing to forecast, so they are stored but not sent to
	// a model.
	Inactive []string

	Skipped []string // text columns: stored, never forecast

	// Identifiers are numeric columns that are labels rather than quantities
	// (Campaign ID). Adding them up or forecasting them is meaningless.
	Identifiers []string
	// Averaged are ratio columns (CTR, conversion rate). They are forecast like
	// anything else, but the account figure is the mean across campaigns rather
	// than their sum. Only set when the file has more than one row per day.
	Averaged []string
	// Settings are numeric columns you choose rather than observe -- budget, bid,
	// target CPA. Stored for the record, never forecast: a forecast of a number
	// you set yourself tells you nothing.
	Settings []string
	// Percent marks columns written with a % sign, so the report can put it back.
	Percent map[string]bool

	Raw        []RawRow // every input row, verbatim, for the history table
	RowsPerDay int      // 1 = a plain daily file; more = one row per campaign
}

// Series returns one entity's metric as points, for storage and charting.
func (d *Data) Series(entity, metric string) []Point {
	col := d.Values[entity][metric]
	out := make([]Point, len(d.Days))
	for i, day := range d.Days {
		out[i] = Point{Day: day, Value: col[i]}
	}
	return out
}

// RawRow is one line of the source file, unparsed.
type RawRow struct {
	Line int
	Day  string
	Data map[string]string
}

// Layouts that mean exactly one thing.
var unambiguousLayouts = []string{
	"2006-01-02",
	"2006/01/02",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z07:00",
	"2 Jan 2006",
	"02 Jan 2006",
	"Jan 2, 2006",
}

// dayFirst / monthFirst are the same shape and cannot be told apart from a single
// row: 01/02/2026 is 1 February to most of the world and 2 January in the US.
// Getting it wrong silently relabels every point in the series, so the choice is
// made once for the whole file in resolveDateOrder.
var dayFirstLayouts = []string{"02/01/2006", "02-01-2006", "02.01.2006"}
var monthFirstLayouts = []string{"01/02/2006", "01-02-2006", "01.02.2006"}

func tryLayouts(s string, layouts []string) (string, bool) {
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// resolveDateOrder picks the layout set for a whole file.
//
// A row where the first number exceeds 12 can only be day-first; one where the
// second does can only be month-first. If nothing in the file settles it, the
// dates are genuinely ambiguous and we refuse rather than pick -- silently
// choosing would move every observation by up to eleven months.
func resolveDateOrder(path string, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return unambiguousLayouts, nil
	}
	if _, ok := tryLayouts(raw[0], unambiguousLayouts); ok {
		return unambiguousLayouts, nil
	}

	dayOK, monthOK := true, true
	for _, s := range raw {
		if _, ok := tryLayouts(s, dayFirstLayouts); !ok {
			dayOK = false
		}
		if _, ok := tryLayouts(s, monthFirstLayouts); !ok {
			monthOK = false
		}
	}
	switch {
	case dayOK && !monthOK:
		return dayFirstLayouts, nil
	case monthOK && !dayOK:
		return monthFirstLayouts, nil
	case dayOK && monthOK:
		return nil, fmt.Errorf("%s: dates like %q could be day/month or month/day and "+
			"nothing in the file settles it. Every date would shift by up to eleven months "+
			"if read the wrong way, so re-export with ISO dates (2026-01-31)", path, raw[0])
	default:
		return unambiguousLayouts, nil // let the per-row error name the bad value
	}
}

func parseDayWith(s string, layouts []string) (string, error) {
	s = strings.TrimSpace(s)
	if d, ok := tryLayouts(s, layouts); ok {
		return d, nil
	}
	// Unambiguous forms are always accepted, so a file may mix 2026-01-02 and
	// 2026/01/02 -- both mean the same thing.
	if d, ok := tryLayouts(s, unambiguousLayouts); ok {
		return d, nil
	}
	return "", fmt.Errorf("unrecognised date %q (expected e.g. 2026-01-31)", s)
}

// nearDuplicates reports whether ignoring case would make the count come out
// right. A campaign exported once as "Brand" and once as "BRAND" is the same
// campaign to everyone except a string comparison, and it is the likeliest
// reason a column has more values than the file has rows per day.
func nearDuplicates(values map[string]bool, want int) string {
	folded := map[string]string{}
	for v := range values {
		k := strings.ToLower(v)
		if first, seen := folded[k]; seen {
			if len(values) == want+1 || len(folded) == want {
				return fmt.Sprintf(".\nTwo of them differ only in capitalisation (%q and "+
					"%q). Make them consistent in the export and try again.", first, v)
			}
		}
		folded[k] = v
	}
	if len(folded) == want && len(values) != want {
		return ".\nSome of them differ only in capitalisation. Make them consistent " +
			"in the export and try again."
	}
	return ""
}

// whyOneColumn guesses why a line did not split, because the two files that
// reach this are both routine: an export saved with a different separator, and a
// platform export that opens with a title and a date range before the header.
// "need at least 2 columns" on its own sends people looking for the wrong thing.
func whyOneColumn(line string) string {
	switch {
	case strings.Contains(line, ";"):
		return ".\nThis line contains semicolons, so the file is probably " +
			"semicolon-separated. Re-export it with commas, or convert it."
	case strings.Contains(line, "\t"):
		return ".\nThis line contains tabs, so the file is probably tab-separated. " +
			"Re-export it as CSV, or convert it."
	default:
		return ".\nIf the file opens with a title or a date range before the header " +
			"row, as platform exports often do, delete those lines and try again."
	}
}

// whereTheDatesAre points at a date column that is not the first one. Column 1
// has to be the date; when it is not, the failure surfaces as "unrecognised date
// \"100\"" on row 2, which describes the symptom and not the cause.
func whereTheDatesAre(header []string) string {
	for i, name := range header {
		if i == 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "day", "date", "days", "dates", "datum", "fecha", "data":
			return fmt.Sprintf(".\nColumn 1 must hold the date, and this file looks "+
				"like it keeps dates in column %d (%q). Move that column first.",
				i+1, name)
		}
	}
	return ""
}

// parseValue reads one cell. The second result says the cell was written as a
// percentage, so the report can put the sign back on.
//
// The number is kept exactly as written -- "4.20%" becomes 4.20, not 0.042 --
// so it round-trips to the page unchanged. Forecasting is unaffected either way.
func parseValue(s string) (float64, error) {
	v, _, err := parseCell(s)
	return v, err
}

func parseCell(s string) (float64, bool, error) {
	s = strings.TrimSpace(s)
	// Tolerate currency symbols, thousands separators, percentages and
	// parenthesised negatives, because real ad-platform exports contain them all.
	neg := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	s = strings.Trim(s, "()")
	pct := strings.HasSuffix(s, "%")
	s = strings.NewReplacer("$", "", "£", "", "€", "", ",", "", "%", "", " ", "").Replace(s)
	if s == "" {
		return 0, pct, fmt.Errorf("empty value")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, pct, fmt.Errorf("not a number: %q", s)
	}
	// ParseFloat accepts "NaN", "Inf" and "Infinity" without complaint. Neither
	// survives storage usefully: NaN violates series.value NOT NULL and surfaces
	// as a raw SQLite constraint error, and Inf is stored silently, then poisons
	// every sum, chart axis and accuracy average that touches the series.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, pct, fmt.Errorf("not a finite number: %q", s)
	}
	if neg {
		v = -v
	}
	return v, pct, nil
}

type row struct {
	day  string
	line int      // for error messages
	raw  []string // values as written; which columns are numbers is decided later
}

// readCSV loads a CSV. want names the columns to keep; empty means every column
// that holds numbers.
//
// Real exports are wide and messy -- "Day, Campaign, Impressions, Clicks, Cost"
// is typical -- so taking column 2 on faith failed on the first real file anyone
// tried. Text columns are set aside rather than causing an error.
func readCSV(path string, want []string, groupBy string) (*Data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Excel writes a UTF-8 BOM, which otherwise becomes part of the first header
	// cell and breaks quoted-field parsing on the very first line.
	br := bufio.NewReader(f)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		br.Discard(3)
	}

	r := csv.NewReader(br)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true

	// Pass 1: read the file. Dates stay as written -- which layout they are in
	// cannot be decided one row at a time.
	var header []string
	type rawRow struct {
		line int
		rec  []string
	}
	var raws []rawRow
	line, width := 0, 0

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line+1, err)
		}
		line++
		if len(rec) < 2 {
			return nil, fmt.Errorf("%s line %d: need at least 2 columns, got %d%s",
				path, line, len(rec), whyOneColumn(rec[0]))
		}
		if line == 1 {
			if _, isDate := tryLayouts(strings.TrimSpace(rec[0]), allLayouts()); !isDate {
				// A file saved as Latin-1 still parses, but its column names arrive
				// mangled ("Coût" becomes "Co�t"), and that name is what gets
				// stored as the metric and printed in the report. Mangling a name
				// silently is the kind of quiet meaning change this refuses.
				for i, name := range rec {
					if !utf8.ValidString(name) {
						return nil, fmt.Errorf("%s: column %d of the header is not valid "+
							"UTF-8, so this file is probably saved in another encoding "+
							"such as Latin-1. Its column names would be stored and "+
							"reported mangled. Re-export or convert it as UTF-8",
							path, i+1)
					}
				}
				header = rec
				continue
			}
		}
		if width == 0 {
			width = len(rec)
		} else if len(rec) != width {
			return nil, fmt.Errorf("%s line %d: has %d columns but earlier rows have %d",
				path, line, len(rec), width)
		}
		raws = append(raws, rawRow{line: line, rec: rec})
	}

	if len(raws) == 0 {
		return nil, fmt.Errorf("%s: no usable rows", path)
	}

	// Pass 2: settle the date layout for the whole file, then parse.
	rawDates := make([]string, len(raws))
	for i, rr := range raws {
		rawDates[i] = strings.TrimSpace(rr.rec[0])
	}
	layouts, err := resolveDateOrder(path, rawDates)
	if err != nil {
		return nil, err
	}

	// A day may repeat. Google Ads exports one row per campaign per day, so a
	// 262-day account with 15 campaigns arrives as 3,930 rows. Those rows are
	// kept verbatim and their numbers are added up per day for forecasting.
	var rows []row
	perDay := map[string]int{}
	for _, rr := range raws {
		day, err := parseDayWith(rr.rec[0], layouts)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w%s", path, rr.line, err,
				whereTheDatesAre(header))
		}
		perDay[day]++
		rows = append(rows, row{day: day, line: rr.line, raw: rr.rec})
	}
	// Every day must have the same number of rows. A day with more or fewer means
	// a campaign started, stopped or is missing, and summing it would put a step
	// in the series that never happened.
	rowsPerDay := perDay[rows[0].day]
	for _, r := range rows {
		if perDay[r.day] != rowsPerDay {
			return nil, fmt.Errorf("%s: %s has %d rows but %s has %d. "+
				"Every day must carry the same rows, or adding them up invents a "+
				"jump in the totals. Re-export a complete date range",
				path, r.day, perDay[r.day], rows[0].day, rowsPerDay)
		}
	}

	// Sort rather than reject: dates are unambiguous, so file order carries no
	// information worth preserving, and newest-first exports are common. Stable,
	// so rows within one day keep the order they were written in.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].day < rows[j].day })

	// Work out which columns hold numbers. A column is usable only if every row
	// in it parses; one stray word makes the whole column text.
	names := make([]string, width-1)
	for c := 1; c < width; c++ {
		names[c-1] = columnName(header, c)
	}
	numeric := make([]bool, width-1)
	pctCol := make([]bool, width-1)
	cols := make([][]float64, width-1)
	for c := range names {
		cols[c] = make([]float64, len(rows))
		bad, firstBad, firstErr := 0, 0, error(nil)
		for i, rw := range rows {
			v, isPct, err := parseCell(rw.raw[c+1])
			if isPct {
				pctCol[c] = true
			}
			if err != nil {
				if bad == 0 {
					firstBad, firstErr = rows[i].line, err
				}
				bad++
				continue
			}
			cols[c][i] = v
		}
		numeric[c] = bad == 0
		// A column that is almost entirely numbers with a few bad cells is a typo,
		// not a text column. Say which line, rather than quietly setting the whole
		// column aside and reporting that nothing can be forecast.
		if bad > 0 && bad*10 < len(rows) {
			return nil, fmt.Errorf("%s line %d: column %d (%s): %w",
				path, firstBad, c+2, names[c], firstErr)
		}
	}

	d := &Data{
		Values:     map[string]map[string][]float64{},
		RowsPerDay: rowsPerDay,
	}

	// Keep every input row exactly as it came in.
	for _, rw := range rows {
		rec := map[string]string{}
		for c := 0; c < len(rw.raw); c++ {
			rec[columnName(header, c)] = strings.TrimSpace(rw.raw[c])
		}
		d.Raw = append(d.Raw, RawRow{Line: rw.line, Day: rw.day, Data: rec})
	}

	// One entry per day, in order.
	for i, rw := range rows {
		if i == 0 || rw.day != rows[i-1].day {
			d.Days = append(d.Days, rw.day)
		}
	}
	dayIndex := map[string]int{}
	for i, day := range d.Days {
		dayIndex[day] = i
	}

	// Sort the numeric columns into what can be forecast and what cannot.
	d.Percent = map[string]bool{}
	mean := map[string]bool{}
	usable := make([]int, 0, len(names))
	// stored is every numeric column kept in the series table, in the same order
	// as `usable`. d.Names is the subset that is actually forecast.
	stored := make([]string, 0, len(names))
	for c, n := range names {
		switch {
		case !numeric[c]:
			d.Skipped = append(d.Skipped, n)
		case looksLikeIdentifier(n):
			d.Identifiers = append(d.Identifiers, n)
		case looksLikeSetting(n):
			// Stored and aggregated like any other number, just never forecast.
			d.Settings = append(d.Settings, n)
			stored = append(stored, n)
			usable = append(usable, c)
		default:
			if pctCol[c] {
				d.Percent[n] = true
			}
			// A ratio is a real series and is forecast like any other. What it is
			// not is addable: fifteen campaigns' click-through rates do not sum to
			// the account's. Combined as a mean across campaigns instead.
			if rowsPerDay > 1 && looksLikeRatio(n) {
				mean[n] = true
				d.Averaged = append(d.Averaged, n)
			}
			if slicesContainsFold(d.Names, n) {
				return nil, fmt.Errorf("%s: two columns are both named %q", path, n)
			}
			d.Names = append(d.Names, n)
			stored = append(stored, n)
			usable = append(usable, c)
		}
	}
	if len(d.Names) == 0 {
		return nil, fmt.Errorf("%s: no column holds numbers that can be forecast "+
			"(text: %s | identifiers: %s | settings: %s)", path,
			listOr(d.Skipped, "none"), listOr(d.Identifiers, "none"),
			listOr(d.Settings, "none"))
	}

	// Work out which column separates the rows of one day, if any.
	group, err := findGroupColumn(path, names, numeric, rows, rowsPerDay, groupBy)
	if err != nil {
		return nil, err
	}
	d.GroupBy = group

	addTo := func(entity string) map[string][]float64 {
		if _, ok := d.Values[entity]; !ok {
			m := map[string][]float64{}
			for _, n := range stored {
				m[n] = make([]float64, len(d.Days))
			}
			d.Values[entity] = m
			d.Entities = append(d.Entities, entity)
		}
		return d.Values[entity]
	}
	addTo(AccountEntity) // always first

	groupCol := -1
	for c, n := range names {
		if n == group {
			groupCol = c
		}
	}
	// A campaign actually called "(account)" would be folded into the total and
	// vanish as an entity of its own. Refuse rather than quietly lose it.
	if groupCol >= 0 {
		for _, rw := range rows {
			if strings.TrimSpace(rw.raw[groupCol+1]) == AccountEntity {
				return nil, fmt.Errorf("%s line %d: a %s is called %q, which is the name "+
					"used for the total across all of them. Forecast by a different column "+
					"instead, e.g. -by \"Campaign ID\"",
					path, rw.line, group, AccountEntity)
			}
		}
	}
	for i, rw := range rows {
		di := dayIndex[rw.day]
		entity := AccountEntity
		if groupCol >= 0 {
			entity = strings.TrimSpace(rw.raw[groupCol+1])
			if entity == "" {
				entity = "(unnamed)"
			}
		}
		for k, c := range usable {
			n := stored[k]
			v := cols[c][i]
			if mean[n] {
				// running mean, so the account figure is the average across the
				// day's campaigns rather than their total
				d.Values[AccountEntity][n][di] += v / float64(rowsPerDay)
			} else {
				d.Values[AccountEntity][n][di] += v
			}
			if entity != AccountEntity {
				addTo(entity)[n][di] += v
			}
		}
	}

	// parseCell keeps non-finite values out of the input, but adding campaigns
	// together is a separate step that can reach infinity on its own. Nothing
	// downstream survives that: SQLite stores Inf without complaint, the model
	// receives it as an input, and every total, chart axis and accuracy average
	// after it is wrong. The invariant is that no stored number is non-finite, so
	// it is checked where the number is actually made.
	for entity, metrics := range d.Values {
		for name, col := range metrics {
			for i, v := range col {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, fmt.Errorf("%s: adding up %q for %s on %s overflowed to %v; "+
						"the individual values are finite but their total is not",
						path, name, entity, d.Days[i], v)
				}
			}
		}
	}

	// An entity whose every metric never changes has nothing to forecast -- the
	// answer is the constant it already is. In a Google Ads export these are the
	// paused campaigns: zero cost, zero clicks, and a budget that never moves.
	// They stay in the raw table and in the series table, and are named in the
	// output, but they are not sent to a model.
	live := d.Entities[:0]
	for _, e := range d.Entities {
		varies := false
		for _, n := range d.Names { // forecastable metrics only, not settings
			col := d.Values[e][n]
			for _, v := range col {
				if v != col[0] {
					varies = true
					break
				}
			}
			if varies {
				break
			}
		}
		if varies || e == AccountEntity {
			live = append(live, e)
		} else {
			d.Inactive = append(d.Inactive, e)
			// kept in d.Values so it is still stored, just not forecast
		}
	}
	d.Entities = live

	if err := checkConsecutiveDays(path, d.Days); err != nil {
		return nil, err
	}
	if len(d.Days) < smallestUsefulSeries {
		return nil, fmt.Errorf("%s: %d days is too few to forecast from; need at least %d",
			path, len(d.Days), smallestUsefulSeries)
	}

	// Restrict to the columns asked for, keeping the order they were asked in.
	if len(want) > 0 {
		chosen := make([]string, 0, len(want))
		for _, w := range want {
			found := ""
			for _, n := range d.Names {
				if strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(w)) {
					found = n
					break
				}
			}
			if found == "" {
				if slicesContainsFold(d.Skipped, w) {
					return nil, fmt.Errorf("%s: column %q holds text, not numbers, so it "+
						"cannot be forecast. Columns that can: %s",
						path, w, listOr(d.Names, "none"))
				}
				// A column can be present and still not forecastable. Saying "no
				// column named" about one the reader can see in their own export
				// sends them looking for a typo instead of reading the rule that
				// excluded it.
				if slicesContainsFold(d.Settings, w) {
					return nil, fmt.Errorf("%s: column %q is a setting -- a number you "+
						"choose rather than observe -- so it is stored but never "+
						"forecast. Give it with -future instead if you know it in "+
						"advance. Columns that can be forecast: %s",
						path, w, listOr(d.Names, "none"))
				}
				if slicesContainsFold(d.Identifiers, w) {
					return nil, fmt.Errorf("%s: column %q looks like a label rather than "+
						"a quantity, so it is stored but never forecast. Columns that "+
						"can be: %s", path, w, listOr(d.Names, "none"))
				}
				return nil, fmt.Errorf("%s: no column named %q. Columns that hold numbers: %s",
					path, w, listOr(d.Names, "none"))
			}
			chosen = append(chosen, found)
		}
		d.Names = chosen
	}
	return d, nil
}

// checkConsecutive refuses a series with missing days.
//
// Both models treat the input as evenly spaced, so a gap is read as continuous
// time and every forecast date after it is wrong. Filling the gap would be
// inventing data, so the only honest options are to refuse and say where.
func checkConsecutiveDays(path string, days []string) error {
	for i := 1; i < len(days); i++ {
		prev, _ := time.Parse("2006-01-02", days[i-1])
		cur, _ := time.Parse("2006-01-02", days[i])
		gap := int(cur.Sub(prev).Hours() / 24)
		if gap != 1 {
			missing := gap - 1
			return fmt.Errorf("%s: %d day(s) missing between %s and %s. "+
				"Both models read the series as consecutive days, so a gap shifts every "+
				"forecast date. Fill the missing days in your data (a real 0 is fine) and re-run",
				path, missing, days[i-1], days[i])
		}
	}
	return nil
}

// allLayouts is every layout we can read, for deciding whether line 1 is a
// header or already data.
func allLayouts() []string {
	out := append([]string{}, unambiguousLayouts...)
	out = append(out, dayFirstLayouts...)
	return append(out, monthFirstLayouts...)
}

// findGroupColumn works out which column separates the rows of a single day.
//
// A Google Ads export carries one row per campaign per day, so a column whose
// distinct values exactly match the number of rows per day is the campaign
// column. Requiring an exact match keeps it from latching onto a status column
// that happens to vary.
func findGroupColumn(path string, names []string, numeric []bool, rows []row,
	rowsPerDay int, asked string) (string, error) {

	if rowsPerDay == 1 {
		if asked != "" {
			return "", fmt.Errorf("%s: -by %q, but this file has one row per day already",
				path, asked)
		}
		return "", nil
	}

	distinct := make([]map[string]bool, len(names))
	for c := range names {
		distinct[c] = map[string]bool{}
		for _, rw := range rows {
			if c+1 < len(rw.raw) {
				distinct[c][strings.TrimSpace(rw.raw[c+1])] = true
			}
		}
	}

	if asked != "" {
		for c, n := range names {
			if strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(asked)) {
				if len(distinct[c]) != rowsPerDay {
					return "", fmt.Errorf("%s: -by %q has %d distinct values but there are "+
						"%d rows per day, so it does not separate them%s",
						path, n, len(distinct[c]), rowsPerDay,
						nearDuplicates(distinct[c], rowsPerDay))
				}
				return n, nil
			}
		}
		return "", fmt.Errorf("%s: no column named %q", path, asked)
	}

	var text, numericCand []string
	for c, n := range names {
		if len(distinct[c]) == rowsPerDay {
			if numeric[c] {
				numericCand = append(numericCand, n)
			} else {
				text = append(text, n)
			}
		}
	}
	// Prefer a text column: "Campaign" reads better than "Campaign ID", and both
	// identify the same thing.
	switch {
	case len(text) == 1:
		return text[0], nil
	case len(text) > 1:
		return "", fmt.Errorf("%s: %d rows per day, and several columns could separate "+
			"them (%s). Choose one with -by NAME", path, rowsPerDay, strings.Join(text, ", "))
	case len(numericCand) == 1:
		return numericCand[0], nil
	case len(numericCand) > 1:
		return "", fmt.Errorf("%s: %d rows per day, and several columns could separate "+
			"them (%s). Choose one with -by NAME", path, rowsPerDay,
			strings.Join(numericCand, ", "))
	}
	return "", fmt.Errorf("%s: there are %d rows per day but no column has exactly %d "+
		"distinct values, so they cannot be told apart. Name the column with -by NAME",
		path, rowsPerDay, rowsPerDay)
}

// looksLikeIdentifier spots numeric columns that label a thing rather than
// measure it. Google Ads exports carry "Campaign ID"; adding fifteen of those
// together produces 327,129,489,016, which is not a number anyone wants.
func looksLikeIdentifier(name string) bool {
	// Match the last WORD, not a bare suffix: "Max CPC bid" ends in "id" and was
	// being read as an identifier, which then kept a real budget setting out of
	// the settings rule.
	fields := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)),
		func(r rune) bool { return r == ' ' || r == '_' || r == '-' })
	if len(fields) == 0 {
		return false
	}
	switch fields[len(fields)-1] {
	case "id", "ids", "code":
		return true
	}
	return false
}

// looksLikeRatio spots columns that are a rate rather than a count. They are
// forecast like anything else; the difference is only in how a day's campaigns
// are combined -- averaged, because fifteen campaigns' click-through rates do
// not add up to the account's.
func looksLikeRatio(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, w := range []string{"ctr", "rate", "%", "ratio", "share",
		"avg.", "avg ", "average"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// looksLikeSetting spots numbers you choose rather than observe. A daily budget
// is a dial you turn; "forecasting" it just replays the number you set, which is
// what it did before this existed -- seven days of 5877.00.
//
// Cost per acquisition is deliberately not here. It is cost divided by
// conversions, an outcome you measure, not a control you set.
func looksLikeSetting(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, w := range []string{"budget", "bid", "target", "limit", "cap"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func slicesContainsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func listOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}

func columnName(header []string, i int) string {
	if i < len(header) && strings.TrimSpace(header[i]) != "" {
		return strings.TrimSpace(header[i])
	}
	if i == 1 {
		return "value"
	}
	return fmt.Sprintf("column%d", i+1)
}

// nextDays returns the n days following the last observation. Forecast rows need
// real dates attached; the models return an unlabelled array.
func nextDays(last string, n int) ([]string, error) {
	t, err := time.Parse("2006-01-02", last)
	if err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := range out {
		t = t.AddDate(0, 0, 1)
		out[i] = t.Format("2006-01-02")
	}
	return out, nil
}
