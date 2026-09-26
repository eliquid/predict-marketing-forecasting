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
	"unicode"
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
	// LabelBy is the column holding the readable name, when GroupBy is an ID
	// column. Empty when the group column is itself the name.
	LabelBy string
	// Label maps a group value to the name shown for it -- the name it carried on
	// the last day it appears. Empty when there is nothing to translate.
	Label map[string]string
	// Renamed records campaigns whose name changed during the period, as
	// "old -> new". Only an ID column can reveal this.
	Renamed []string
	// Values is entity -> metric -> one value per day.
	Values map[string]map[string][]float64
	// Inactive lists entities that are stored but not sent to a model: everything
	// in Paused, plus anything whose every metric is constant for the whole
	// period. Both are kept in full in the raw and series tables.
	Inactive []string
	// Paused is the subset of Inactive that the export itself says is switched
	// off, read from its campaign-status column. Kept separate only so the output
	// can give the right reason.
	Paused []string
	// Stopped is the other subset of Inactive: entities the export stopped
	// listing before its last day. Only -fill-absent can see this, because only
	// a ragged export leaves a gap to notice.
	Stopped []string

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
	// NotMetrics are numeric columns that are not one of the metrics this tool
	// forecasts (wantedMetrics). Stored, named on screen, never forecast.
	NotMetrics []string
	// Currency is the export's single currency, when it says. Empty when the file
	// carries no currency column.
	Currency string
	// WeightedBy names the column each blended rate was weighted by, empty when it
	// fell back to an unweighted mean. The difference changes the number by
	// multiples, so the output has to say which happened.
	WeightedBy map[string]string
	// Concept maps each forecast column to which wanted metric it matched, so the
	// output can show the reasoning rather than just the verdict.
	Concept map[string]string
	// Unfiltered is true when the allow-list was not applied -- either -columns
	// named the metrics outright, or nothing in the file matched it at all.
	Unfiltered bool
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

// sniffNotACSV looks at the first bytes before anything tries to read the file as
// text, and names the file it actually is.
//
// Every one of these used to arrive as "line 1: need at least 2 columns, got 1",
// followed by advice to delete the title rows -- impossible advice for a workbook,
// which has no lines to delete, and the wrong advice for a UTF-16 export, where
// deleting them does not help either. Renaming a workbook to .csv instead of
// re-saving it is one of the most common mistakes there is, and it was getting the
// least useful message of any of them.
//
// Tabs are checked across the first lines, not just the first: a platform export
// opens with a title and a date range, and those rows have no tabs in them, so
// looking only at line 1 hid the one message that would have helped. That is
// exactly the file the README calls the most common reason a real export will not
// load, and it was the one the check missed.
func sniffNotACSV(path string, br *bufio.Reader) error {
	head, _ := br.Peek(8192)
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return fmt.Errorf("%s is an Excel workbook (or another zip file), not a CSV. "+
			"Renaming a .xlsx to .csv does not convert it. Open it and use "+
			"File > Save As, choosing CSV", path)
	case bytes.HasPrefix(head, []byte("%PDF")):
		return fmt.Errorf("%s is a PDF, not a CSV. Download the export again and "+
			"choose the CSV format", path)
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}), bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		return fmt.Errorf("%s is UTF-16, not a CSV this can read. In Google Ads this "+
			"is the \".csv (Excel)\" download, which is tab-separated UTF-16 despite "+
			"the name -- take the plain \".csv\" one instead. To convert what you have: "+
			"iconv -f UTF-16 -t UTF-8 %q | tr '\\t' ',' > fixed.csv", path, path)
	}

	// Separator, judged over the first lines rather than only the first.
	lines := strings.SplitN(string(head), "\n", 25)
	tabs, semis, commas := 0, 0, 0
	for _, l := range lines {
		tabs += strings.Count(l, "\t")
		semis += strings.Count(l, ";")
		commas += strings.Count(l, ",")
	}
	if commas == 0 && tabs > 0 {
		return fmt.Errorf("%s has tabs and no commas, so it is tab-separated, not a "+
			"CSV. In Google Ads this is the \".csv (Excel)\" download -- take the plain "+
			"\".csv\" one instead, or convert it: tr '\\t' ',' < %q > fixed.csv", path, path)
	}
	if commas == 0 && semis > 0 {
		return fmt.Errorf("%s has semicolons and no commas, so it is semicolon-separated. "+
			"Re-export it with commas, or convert it", path)
	}
	return nil
}

// clean strips control characters from a value that came out of the file.
//
// A campaign name is data, and it was being printed straight to the terminal. A
// name containing ESC[2K ESC[1G erased the line above it as it was written, and
// the line it erased was `for N: ...` -- the one naming which campaigns are in the
// forecast, printed by the same mechanism as the "switched off in the export"
// warning. A carriage return did the same thing. The truncator could also cut a
// name mid-escape, leaving the colour set for the rest of the session.
//
// Tabs and newlines go too: both break the column layout the output is read in.
func clean(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
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

// noData is every way an export says it has no value for a cell. All of them
// mean zero, and none of them is an error.
//
// A blank is overwhelmingly a day the campaign did not spend, but it also turns
// up on a day that spent and recorded no conversions, and on a day that recorded
// conversions against no spend. All three are the same fact: nothing happened, so
// the number is 0. Reading them any other way cost a real import four of its
// metrics -- 11 blank cells in 1,392 rows demoted "Unique link clicks" to a text
// column, and a demoted column simply stops appearing in the forecast.
//
// A stray word is still an error, and deliberately: that is a typo or the wrong
// file, and the line number is worth more than a silent zero.
var noData = map[string]bool{
	"": true, "-": true, "--": true, "---": true, "\u2013": true, "\u2014": true,
	"n/a": true, "na": true, "nan": true, "null": true, "nil": true, "none": true,
}

// normaliseCell strips everything that is decoration -- currency, percent sign,
// spaces of every width, a parenthesised negative -- and settles which separator
// is the decimal point. parseCell and cellIsNoData both go through it so they can
// never disagree about what a cell says.
func normaliseCell(s string) (string, bool, bool) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	s = strings.Trim(s, "()")
	pct := strings.HasSuffix(s, "%")
	s = strings.NewReplacer("$", "", "£", "", "€", "", "¥", "", "₹", "", "%", "").Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	dot, comma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	euro := (dot >= 0 && comma > dot) ||
		(comma >= 0 && dot < 0 && len(s)-comma-1 != 3)
	if euro {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	return s, pct, neg
}

// cellIsNoData reports whether a cell is one of the ways an export says it has
// nothing for it. parseCell reads those as 0, which is right for a count but not
// for a rate: a campaign that reported no cost-per-purchase has no rate to
// average, and counting it as a rate of zero pulled the account figure down.
func cellIsNoData(s string) bool {
	t, _, _ := normaliseCell(s)
	return noData[strings.ToLower(t)]
}

func parseCell(s string) (float64, bool, error) {
	s, pct, neg := normaliseCell(s)

	if strings.ContainsAny(s, "xX_") {
		return 0, pct, fmt.Errorf("not a number: %q", s)
	}

	if noData[strings.ToLower(s)] {
		return 0, pct, nil // no data is 0, not a failure
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, pct, fmt.Errorf("not a number: %q", s)
	}
	// "NaN" is handled above as no data. Infinity is not: it is a division that
	// went wrong rather than a measurement that is missing, it is stored silently,
	// and it then poisons every sum, chart axis and accuracy average that touches
	// the series. NaN is still checked here because ParseFloat reaches it by other
	// spellings than the one noData lists.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, pct, fmt.Errorf("not a finite number: %q", s)
	}
	if neg {
		v = -v
	}
	return v, pct, nil
}

// Bounds on the shape of an export. Neither is a judgement about anyone's data:
// they exist because growth on the ingest path is multiplicative -- entities times
// metrics times charts, all inlined into one HTML file -- so a wide or malformed
// file degrades into swap rather than into a message. Measured before they
// existed: a 850 KB file of 5,000 columns reached 3.9 GB resident and produced a
// 12 MB single-page report. Real exports are nowhere near these: the Google one
// here has 13 columns, the Meta one 12.
const (
	maxColumns   = 512
	maxCellBytes = 1 << 20
)

// filledLine marks a row -fill-absent synthesised rather than read from the file.
const filledLine = -1

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
	return readCSVFilling(path, want, groupBy, false)
}

// readCSVFilling is readCSV with the -fill-absent rule turned on or off.
func readCSVFilling(path string, want []string, groupBy string, fillAbsent bool) (*Data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	if err := sniffNotACSV(path, br); err != nil {
		return nil, err
	}

	// Excel writes a UTF-8 BOM, which otherwise becomes part of the first header
	// cell and breaks quoted-field parsing on the very first line.
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
		for _, cell := range rec {
			if len(cell) > maxCellBytes {
				return nil, fmt.Errorf("%s line %d: a single value is %d bytes, over the "+
					"%d-byte limit. A real export does not contain one; this file is "+
					"malformed or is not the export you meant",
					path, line, len(cell), maxCellBytes)
			}
		}
		if len(rec) > maxColumns {
			return nil, fmt.Errorf("%s line %d: %d columns, over the limit of %d. Every "+
				"column becomes a series per campaign and a chart in the report, so a "+
				"file this wide turns into gigabytes. Export the columns you need",
				path, line, len(rec), maxColumns)
		}
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
				// No date in the first cell. If no cell in this row is a date
				// either, the file has no dates in it at all -- a summary export
				// with one row per campaign, which is a different download rather
				// than a broken one. Saying "unrecognised date \"Brand Search\""
				// points at the campaign name and reads as though the names are
				// wrong.
				if line == 1 {
					header = rec
					continue
				}
			}
		}
		if line == 2 {
			anyDate := false
			for _, cell := range rec {
				if _, ok := tryLayouts(strings.TrimSpace(cell), allLayouts()); ok {
					anyDate = true
					break
				}
			}
			if !anyDate {
				return nil, fmt.Errorf("%s: no column holds a date. This looks like a "+
					"summary export -- one row per campaign, with no day on it -- and "+
					"there is no time series in it to forecast. Download it again "+
					"segmented by day", path)
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
		// Cleaned here, where the row enters, and not later where the entity is
		// built. Every uniqueness guarantee -- sameCampaignsEveryDay, the "(account)"
		// refusal, entityLabels' disambiguation -- is established on this string, so
		// cleaning afterwards meant they were all proved about a different value
		// than the one used. One invisible byte walked past all three: a campaign
		// called "\x01(account)" was folded into the total and vanished, and "Alpha"
		// plus "Alpha\x01" became one campaign holding both their numbers.
		for i, cell := range rr.rec {
			rr.rec[i] = clean(cell)
		}
		rows = append(rows, row{day: day, line: rr.line, raw: rr.rec})
	}
	// stoppedEntities are those whose rows end before the file does -- known to
	// have stopped, not merely quiet. Only -fill-absent can tell.
	var stoppedEntities []string

	// Some platforms emit a row for every entity on every day, zero-filled, and
	// some emit a row only where there was activity. fillAbsent handles the
	// second shape; without it, every day must carry the same number of rows.
	if fillAbsent {
		filled, gone, note, err := fillAbsentRows(path, header, rows, layouts, groupBy)
		if err != nil {
			return nil, err
		}
		stoppedEntities = gone
		if note != "" {
			fmt.Print(note)
		}
		rows = filled
		perDay = map[string]int{}
		for _, r := range rows {
			perDay[r.day]++
		}
	}

	// Every day must have the same number of rows. A day with more or fewer means
	// an entity started, stopped or is missing, and summing it would put a step
	// in the series that never happened.
	rowsPerDay := perDay[rows[0].day]
	for _, r := range rows {
		if perDay[r.day] != rowsPerDay {
			return nil, fmt.Errorf("%s: %s has %d rows but %s has %d. "+
				"Every day must carry the same rows, or adding them up invents a "+
				"jump in the totals. Re-export a complete date range, or pass "+
				"-fill-absent if this export only lists an entity on the days it "+
				"was running",
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

	// Keep every input row exactly as it came in. Rows -fill-absent synthesised
	// are not input rows and never reach the raw table: raw is the record of what
	// the platform actually sent, and a zero it never sent does not belong in it.
	for _, rw := range rows {
		if rw.line == filledLine {
			continue
		}
		rec := map[string]string{}
		for c := 0; c < len(rw.raw); c++ {
			rec[columnName(header, c)] = clean(strings.TrimSpace(rw.raw[c]))
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

	// Which numeric columns are metrics this tool forecasts. An explicit -columns
	// is the user saying so directly, and overrides the allow-list entirely.
	//
	// If nothing matches, the allow-list is not applied at all. A plain two-column
	// series ("date,v") names its metric whatever the person liked, and there is
	// nothing to filter in a file with one number in it -- filtering there would
	// refuse the simplest possible input to buy nothing. The list earns its keep on
	// a platform export, which is exactly the file that has columns nobody asked
	// for. d.Unfiltered records that this happened so the caller can say so.
	concept := map[string]string{}
	notAddable := map[string]bool{}
	anyWanted := false
	for c, n := range names {
		if !numeric[c] || looksLikeIdentifier(n) || looksLikeSetting(n) {
			continue
		}
		if cn, addable, ok := metricConcept(n); ok {
			concept[n], anyWanted = cn, true
			if !addable {
				notAddable[n] = true
			}
		}
	}
	filter := anyWanted && len(want) == 0
	d.Unfiltered = !filter

	// Sort the numeric columns into what can be forecast and what cannot.
	d.Percent = map[string]bool{}
	d.Concept = map[string]string{}
	d.WeightedBy = map[string]string{}
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
		case filter && concept[n] == "":
			// Numeric, but not one of the things this tool forecasts. Kept in raw
			// like everything else, and named on screen -- never dropped quietly,
			// because a column that silently stops being forecast is exactly the
			// failure this list exists to end.
			d.NotMetrics = append(d.NotMetrics, n)
		default:
			if pctCol[c] {
				d.Percent[n] = true
			}
			d.Concept[n] = concept[n]
			// A ratio is a real series and is forecast like any other. What it is
			// not is addable: fifteen campaigns' click-through rates do not sum to
			// the account's. Combined as a mean across campaigns instead.
			if rowsPerDay > 1 && (notAddable[n] || looksLikeRatio(n)) {
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
			"(text: %s | identifiers: %s | settings: %s | not metrics: %s)", path,
			listOr(d.Skipped, "none"), listOr(d.Identifiers, "none"),
			listOr(d.Settings, "none"), listOr(d.NotMetrics, "none"))
	}

	// Before the group column is chosen: a currency column is often one of the
	// candidates, so a mixed-currency file would otherwise be refused as
	// "several columns could separate them" rather than for the reason that
	// matters.
	if err := oneCurrency(path, d); err != nil {
		return nil, err
	}

	// Work out which column separates the rows of one day, if any.
	group, err := findGroupColumn(path, names, numeric, rows, rowsPerDay, groupBy)
	if err != nil {
		return nil, err
	}
	d.GroupBy = group

	// A campaign can be renamed. Its ID does not change, so when the export
	// carries one the history stays a single series and the name shown is simply
	// the latest one. Without an ID there is no way to tell a rename from one
	// campaign ending and another starting, and the tool does not guess.
	if group != "" && looksLikeIdentifier(group) {
		d.LabelBy, d.Label, d.Renamed = entityLabels(header, names, numeric, rows,
			group, rowsPerDay)
	}

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

	// Which column is the natural denominator of each rate, so the account figure
	// can be the real blended one. Empty when the file does not carry it, and the
	// mean is then unweighted over the campaigns that actually reported.
	rateWeight := map[string]int{}
	rateNum := map[string][]float64{}
	rateDen := map[string][]float64{}
	for k, n := range stored {
		if !mean[n] {
			continue
		}
		rateNum[n] = make([]float64, len(d.Days))
		rateDen[n] = make([]float64, len(d.Days))
		want := rateDenominator(n)
		if want == "" {
			continue
		}
		// The best column of that concept, not the first one in the file. Taking
		// the first made the blended figure depend on the order the columns were
		// downloaded in: on a Meta header where "Reach" and "Landing page views"
		// precede "Impressions", account CTR came out 1.95% against a true 1.00%,
		// and swapping two columns in the same file changed it to 1.000.
		//
		// Ranked by how canonical the name is -- patternRank is the position of the
		// matched pattern in its concept's list, which is written most-canonical
		// first, so "Impressions" beats "Landing page views" beats "Reach" -- then
		// by the shorter name, so plain "Clicks" beats "Outbound clicks".
		// Two ways a column qualifies. Either its name contains the denominator the
		// rate names -- "Cost per add to cart" wants an adds-to-cart column -- or it
		// is the canonical column for the concept, meaning it matched that concept's
		// first pattern ("Impressions", "Clicks"), which is what a bare CTR or CPC
		// means by its denominator.
		//
		// Anything else is a guess and is refused, because a plausible wrong weight
		// is worse than no weight: on the real Meta export, "Cost per add to cart"
		// was being weighted by "Website purchases" -- the right concept, the wrong
		// column -- for a gap of up to 31.9% against the honest unweighted mean.
		phrase := denominatorPhrase(n)
		best, bestRank, bestLen := -1, 1<<30, 1<<30
		for k2, n2 := range stored {
			if k2 == k || d.Concept[n2] != want || mean[n2] {
				continue
			}
			r := patternRank(n2, want)
			if phrase != "" && strings.Contains(normaliseColumn(n2), " "+phrase+" ") {
				r = -1 // names the very thing the rate is per
			} else if r >= canonCount(want) {
				continue // not canonical and not named: not a weight, a guess
			}
			if r < bestRank || (r == bestRank && len(n2) < bestLen) {
				best, bestRank, bestLen = usable[k2], r, len(n2)
			}
		}
		if best >= 0 {
			rateWeight[n] = best
			for k2, n2 := range stored {
				if usable[k2] == best {
					d.WeightedBy[n] = n2
				}
			}
		}
	}

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
			v := strings.TrimSpace(rw.raw[groupCol+1])
			if lbl, ok := d.Label[v]; ok {
				v = lbl
			}
			if v == AccountEntity {
				return nil, fmt.Errorf("%s line %d: a %s is called %q, which is the name "+
					"used for the total across all of them. Forecast by a different column "+
					"instead, e.g. -by \"Campaign ID\"",
					path, rw.line, group, AccountEntity)
			}
		}
	}
	// Counting rows per day is not enough. A day that lists one campaign twice and
	// another not at all has the right number of rows and goes straight through:
	// the duplicate is added to itself, and the missing campaign is stored as a
	// real zero it never reported. Measured on a two-campaign file, one such day
	// stored 1119 against a true 120 for the campaign that appeared twice, and 0
	// for the one that did not appear -- a fabricated step in exactly the series
	// the row count exists to protect. The set has to be checked, not the size.
	if groupCol >= 0 {
		if err := sameCampaignsEveryDay(path, rows, groupCol, group); err != nil {
			return nil, err
		}
	}

	for i, rw := range rows {
		di := dayIndex[rw.day]
		entity := AccountEntity
		if groupCol >= 0 {
			entity = clean(strings.TrimSpace(rw.raw[groupCol+1]))
			if lbl, ok := d.Label[entity]; ok {
				entity = lbl // the name it goes by now, not the ID or an older name
			}
			if entity == "" {
				entity = "(unnamed)"
			}
		}
		for k, c := range usable {
			n := stored[k]
			v := cols[c][i]
			if mean[n] {
				// A rate does not add up across campaigns, and it does not plainly
				// average either: the account's CTR is total clicks over total
				// impressions, not the mean of each campaign's CTR. Weighting each
				// campaign's rate by its own denominator gives exactly that, because
				// sum(rate_i * w_i) / sum(w_i) is sum(numerator_i) / sum(w_i).
				//
				// Measured on a three-campaign day, unweighted against the true
				// blended figure: CTR 4.7167 vs 4.8869, Avg. CPC 1.6267 vs 2.1975
				// (26% low), ROAS 2.9167 vs 3.5292. A blended CPC reported a quarter
				// low reads as headroom to bid up.
				w := 1.0
				if wc, ok := rateWeight[n]; ok {
					w = cols[wc][i]
				} else if rw.line == filledLine || cellIsNoData(rw.raw[c+1]) {
					// No weight column to fall back on, so the mean is unweighted --
					// over the campaigns that actually reported a rate. A row
					// -fill-absent invented never reported one, and neither did a
					// campaign whose cell is blank or "-". Counting either as a rate
					// of zero pulled the account figure down: three campaigns at 20,
					// 40 and "-" gave 20.00 where the mean of those that reported is
					// 30.00, and the bigger the campaign with nothing to report, the
					// lower the account's apparent cost per purchase.
					w = 0
				}
				rateNum[n][di] += v * w
				rateDen[n][di] += w
			} else {
				d.Values[AccountEntity][n][di] += v
			}
			if entity != AccountEntity {
				addTo(entity)[n][di] += v
			}
		}
	}

	// The weighted rates, now that every campaign has been seen.
	for n, num := range rateNum {
		for di := range num {
			if den := rateDen[n][di]; den != 0 {
				d.Values[AccountEntity][n][di] = num[di] / den
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

	// Two reasons an entity is stored but never sent to a model.
	//
	// The export says it is switched off. A paused campaign's next seven days are
	// a decision, not a forecast: it will spend nothing until someone turns it
	// back on. Asking a model anyway returns noise hovering around zero whose
	// quantiles come back in no particular order, which fails the sanity check in
	// checkForecast and takes the whole report down with it. One long-dead
	// campaign did exactly that.
	//
	// Or every metric it records is constant for the whole period, so the answer
	// is the constant it already is. This catches a file with no status column,
	// and a campaign switched on but never funded.
	//
	// Either way it stays in the raw table and in the series table, and is named
	// in the output. Nothing is dropped from the database.
	running := runningEntities(d)
	live := d.Entities[:0]
	for _, e := range d.Entities {
		if e == AccountEntity {
			live = append(live, e)
			continue
		}
		if slicesContainsFold(stoppedEntities, e) || slicesContainsFold(stoppedEntities, labelKey(d, e)) {
			d.Stopped = append(d.Stopped, e)
			d.Inactive = append(d.Inactive, e)
			continue
		}
		if running != nil && !running[e] {
			d.Paused = append(d.Paused, e)
			d.Inactive = append(d.Inactive, e)
			continue // kept in d.Values so it is still stored, just not forecast
		}
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
		if varies {
			live = append(live, e)
		} else {
			d.Inactive = append(d.Inactive, e)
		}
	}
	d.Entities = live

	if err := checkConsecutiveDays(path, d.Days); err != nil {
		return nil, err
	}
	if len(d.Days) < smallestUsefulSeries {
		return nil, tooShort{Path: path, Days: len(d.Days)}
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

// fillAbsentRows adds a zero row for every (day, entity) the file leaves out,
// but only where the entity's absence means it did not exist yet or had already
// stopped.
//
// Reporting exports come in two shapes. Some emit a row for every entity on
// every day and put zeros in it; some emit a row only where there was activity,
// so the day count rises and falls as entities start and stop. The second shape
// is not a broken export, but it is indistinguishable from one by row count
// alone, and summing a day with six entities against a day with ten puts a step
// in the account total that never happened.
//
// What separates the two is *where* the absence falls:
//
//   - before an entity's first row, or after its last: it was not running. Zero
//     is what it spent, and filling it in states a fact the export implied.
//   - between its first and last row: the entity was running and a day is
//     missing. That is a hole in the data, nothing can be inferred, and it is
//     still refused.
//
// The entity column is found by the same rule used for a dense file -- a label
// that identifies a row within its day -- except that here the count of distinct
// values exceeds any single day's row count, which is exactly the symptom.
func fillAbsentRows(path string, header []string, rows []row, layouts []string, groupBy string) ([]row, []string, string, error) {
	col := uniquePerDayColumn(header, rows, groupBy)
	if col < 0 {
		return nil, nil, "", fmt.Errorf("%s: -fill-absent needs a column naming the thing "+
			"each row is about, and no column identifies a row within its day. "+
			"Name it with -by if it is one of these: %s", path, strings.Join(header, ", "))
	}

	days := map[string]bool{}
	first, last := map[string]string{}, map[string]string{}
	seen := map[string]bool{} // day\x1fentity
	template := map[string]row{}
	for _, r := range rows {
		e := strings.TrimSpace(r.raw[col])
		days[r.day] = true
		seen[r.day+"\x1f"+e] = true
		if f, ok := first[e]; !ok || r.day < f {
			first[e] = r.day
		}
		if l, ok := last[e]; !ok || r.day > l {
			last[e] = r.day
		}
		if _, ok := template[e]; !ok {
			template[e] = r
		}
	}

	ordered := make([]string, 0, len(days))
	for d := range days {
		ordered = append(ordered, d)
	}
	sort.Strings(ordered)

	// A day missing from inside an entity's own run is a hole, not an absence.
	for e := range first {
		for _, d := range ordered {
			if d < first[e] || d > last[e] {
				continue
			}
			if !seen[d+"\x1f"+e] {
				return nil, nil, "", fmt.Errorf("%s: %q has no row for %s, which is inside "+
					"its own run (%s to %s). A day missing from the middle is a gap in "+
					"the export, not an entity that was not running, and nothing can be "+
					"inferred for it. Re-export a complete range",
					path, e, d, first[e], last[e])
			}
		}
	}

	added := 0
	byEntity := map[string]int{}
	for e, t := range template {
		for _, d := range ordered {
			if seen[d+"\x1f"+e] {
				continue
			}
			rec := make([]string, len(t.raw))
			copy(rec, t.raw)
			rec[0] = d
			for c := 1; c < len(rec); c++ {
				switch {
				case c == col:
					// the entity's own name, kept
				case looksLikeIdentifier(columnName(header, c)):
					// a label for the entity, not a quantity: keep it
				default:
					if _, ok := tryLayouts(strings.TrimSpace(rec[c]), layouts); ok {
						rec[c] = d // a second date column tracks the day
					} else if _, _, err := parseCell(rec[c]); err == nil {
						rec[c] = "0" // it spent nothing, because it was not running
					}
				}
			}
			rows = append(rows, row{day: d, line: filledLine, raw: rec})
			added++
			byEntity[e]++
		}
	}
	// An entity whose rows stop before the file does has stopped running. That is
	// a fact about the export's shape, not an inference from its values: the
	// platform had nothing to report for it. Forecasting a tail of zeros produces
	// noise around zero whose quantiles come back in no order, which fails
	// checkForecast and takes the whole run down -- so it is excluded for the same
	// reason a status column marks one paused (AGENTS.md 2a1).
	lastDay := ordered[len(ordered)-1]
	var stopped []string
	for e, l := range last {
		if l < lastDay {
			stopped = append(stopped, e)
		}
	}
	sort.Strings(stopped)

	if added == 0 {
		return rows, stopped, "", nil
	}

	var names []string
	for e := range byEntity {
		names = append(names, fmt.Sprintf("%s (%d)", e, byEntity[e]))
	}
	sort.Strings(names)
	note := fmt.Sprintf("  -fill-absent: added %d zero row(s) for days outside each "+
		"entity's own run: %s\n", added, strings.Join(names, ", "))
	return rows, stopped, note, nil
}

// uniquePerDayColumn finds the column that names what each row is about: the one
// whose value identifies a row within its day. Text is preferred over a numeric
// identifier, the same order a dense file uses.
func uniquePerDayColumn(header []string, rows []row, groupBy string) int {
	best := -1
	for c := 1; c < len(header); c++ {
		if groupBy != "" && !strings.EqualFold(columnName(header, c), groupBy) {
			continue // the user named the column; only it is allowed to be the label
		}
		seen := map[string]bool{}
		values := map[string]bool{}
		ok := true
		for _, r := range rows {
			if c >= len(r.raw) {
				ok = false
				break
			}
			v := strings.TrimSpace(r.raw[c])
			if v == "" {
				ok = false
				break
			}
			values[v] = true
			k := r.day + "\x1f" + v
			if seen[k] {
				ok = false
				break
			}
			seen[k] = true
		}
		if !ok || len(values) < 2 {
			continue
		}
		name := columnName(header, c)
		if _, _, err := parseCell(rows[0].raw[c]); err != nil {
			return c // a text column: the best kind of label
		}
		if looksLikeIdentifier(name) && best < 0 {
			best = c
		}
	}
	return best
}

// tooShort is returned when a file has fewer days than any model can use.
//
// It carries the count because the floor a reader should be told about is not
// always this one: `forecast` refuses below smallestUsefulSeries, but `import`
// refuses below importMinDays, and quoting 32 at someone whose real problem is
// that they need 90 sends them back with a file that will be refused again.
type tooShort struct {
	Path string
	Days int
}

func (e tooShort) Error() string {
	return fmt.Sprintf("%s: %d days is too few to forecast from; need at least %d",
		e.Path, e.Days, smallestUsefulSeries)
}

// checkConsecutive refuses a series with missing days.
//
// Both models treat the input as evenly spaced, so a gap is read as continuous
// time and every forecast date after it is wrong. Filling the gap would be
// inventing data, so the only honest options are to refuse and say where.
func checkConsecutiveDays(path string, days []string) error {
	// Every step the same size, and bigger than a day, is not a file with holes in
	// it: it is an export segmented by week or by month. Telling someone to fill
	// the gaps with zeros is then the worst possible advice -- it makes six days in
	// every seven a real zero, and the forecast follows them to the floor. Measured
	// on a weekly file filled that way: day 1 came back at 1,037.93 and every day
	// after it between 1.59 and 3.49, with negative lower bounds.
	steps := map[int]bool{}
	for i := 1; i < len(days); i++ {
		prev, _ := time.Parse("2006-01-02", days[i-1])
		cur, _ := time.Parse("2006-01-02", days[i])
		steps[int(cur.Sub(prev).Hours()/24)] = true
	}
	if len(steps) == 1 && len(days) > 2 {
		for step := range steps {
			if step == 1 {
				break
			}
			period := fmt.Sprintf("every %d days", step)
			switch {
			case step == 7:
				period = "weekly"
			case step >= 28 && step <= 31:
				period = "monthly"
			}
			return fmt.Errorf("%s: every row is %d days after the one before it, so "+
				"this export is segmented %s, not daily. Download it again segmented "+
				"by day. Do not fill the gaps with zeros: that would make %d days in "+
				"every %d a real zero, and the forecast would follow them down",
				path, step, period, step-1, step)
		}
	}

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

	// A quantity is never a label. "Cost" having exactly as many distinct values
	// as there are campaigns is a coincidence, and grouping by it turns prices
	// into campaign names and removes the metric from the forecast entirely.
	// Classification has already run by the time this is called, so the question
	// can simply be asked: a numeric column is a candidate only when it looks
	// like an identifier ("Campaign ID"), never when it is a metric or a setting.
	label := func(c int, n string) bool { return !numeric[c] || looksLikeIdentifier(n) }

	if asked != "" {
		for c, n := range names {
			if !strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(asked)) {
				continue
			}
			if !label(c, n) {
				return "", fmt.Errorf("%s: -by %q, but %q holds numbers that are "+
					"measured, not a name. Splitting on it would turn its values into "+
					"campaign names and drop it from the forecast. Use the campaign "+
					"column, or its ID", path, n, n)
			}
			if len(distinct[c]) != rowsPerDay {
				return "", fmt.Errorf("%s: -by %q has %d distinct values but there are "+
					"%d rows per day, so it does not separate them%s",
					path, n, len(distinct[c]), rowsPerDay,
					nearDuplicates(distinct[c], rowsPerDay))
			}
			return n, nil
		}
		return "", fmt.Errorf("%s: no column named %q", path, asked)
	}

	var text, ids, measured []string
	for c, n := range names {
		if len(distinct[c]) != rowsPerDay {
			continue
		}
		switch {
		case !numeric[c]:
			text = append(text, n)
		case looksLikeIdentifier(n):
			ids = append(ids, n)
		default:
			measured = append(measured, n) // counted only so the refusal can say so
		}
	}
	tooMany := func(cands []string) error {
		return fmt.Errorf("%s: %d rows per day, and several columns could separate "+
			"them (%s). Choose one with -by NAME", path, rowsPerDay,
			strings.Join(cands, ", "))
	}
	// Prefer a text column: "Campaign" reads better than "Campaign ID", and both
	// identify the same thing.
	switch {
	case len(text) == 1:
		return text[0], nil
	case len(text) > 1:
		return "", tooMany(text)
	case len(ids) == 1:
		return ids[0], nil
	case len(ids) > 1:
		return "", tooMany(ids)
	}
	// Nothing had exactly rowsPerDay distinct values. A renamed campaign is exactly
	// this: the name column names each day's rows once but carries an extra value
	// across the file. Accept a column that is unique *within* each day, which is
	// the property that actually matters, before giving up.
	if c := uniquePerDayColumn(append([]string{""}, names...), rows, ""); c >= 0 {
		return names[c-1], nil
	}

	// Nothing that names anything. Say whether the problem is that no column fits
	// at all, or that the only ones that fit are quantities -- they need different
	// answers from the reader, and the second used to be taken silently.
	if len(measured) > 0 {
		return "", fmt.Errorf("%s: there are %d rows per day, and the only column(s) "+
			"with %d distinct values (%s) hold measured numbers rather than names. "+
			"Splitting on one would turn its values into campaign names and drop it "+
			"from the forecast. Add the campaign column to the export, or name a "+
			"label column with -by NAME",
			path, rowsPerDay, rowsPerDay, strings.Join(measured, ", "))
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

// wantedMetrics is the allow-list of things this tool forecasts, in the order it
// is tested. First match wins, and the order is load-bearing, not cosmetic.
//
// Why an allow-list. The old rule was the other way round: forecast every numeric
// column unless something excluded it. That fails open, and an export is not
// something you control -- a platform sends the columns it wants to send. Every
// unwanted column then had to be excluded by name, one rule at a time, and each
// new platform brought a new leak: `Budget`, then `Campaign ID`, then a
// `Budget name` that was blank in all 16,485 rows and became a numeric column of
// zeros. Failing closed is the only way that ends.
//
// addable is false for a per-unit cost or a rate. Fifteen campaigns' costs add up
// to the account's; fifteen campaigns' cost-per-purchase does not. This is not a
// refinement: `Cost per add to cart (USD)` and `Cost per results` were summed on a
// real export, because looksLikeRatio knew only "ctr", "rate", "%", "ratio",
// "share" and "avg", and a sum of per-unit costs is not a number that means
// anything.
// canon is how many of the leading patterns are plain names for the concept
// itself, as opposed to a particular kind of it. "Impressions", "Impr." and "Imps"
// all just mean impressions; "Views" and "Plays" are specific things that are
// counted like impressions. Only a canonical column can serve as a rate's
// denominator, which is what stops a cost-per-add-to-cart being weighted by a
// purchases column that happens to share the concept.
var wantedMetrics = []struct {
	concept  string
	addable  bool
	canon    int
	patterns []string
}{
	// Before "spend", or every cost-per-something reads as spend. Before
	// "conversions", or "cost per conversion" reads as a conversion count.
	{"cost per", false, 0, []string{"cpa", "cpc", "cpm", "cpv", "cpl", "cost per",
		"cost pe", "spend per", "revenue per", "value per"}},
	// Before "clicks" and "conversions", so "click-through rate" and "conversion
	// rate" are rates rather than counts.
	{"rate", false, 0, []string{"ctr", "cvr", "roas", "rate", "ratio", "share",
		"percent", "frequency", "avg", "average", "mean"}},
	// Before "conversions", or "conversion value" is counted as conversions.
	// "conv value" as well as "conversion value": Google Ads writes its revenue
	// column "Conv. value", which normalises to "conv value" and was matching
	// "conv" from the conversions group below -- revenue counted as a conversion
	// count. Both are additive so no total was wrong, but the label was.
	{"revenue", true, 7, []string{"revenue", "conversion value", "conversions value",
		"conv value", "convs value", "purchase value", "purchases value", "sales",
		"turnover"}},
	{"conversions", true, 3, []string{"conversions", "conversion", "conv", "purchases",
		"purchase", "results", "result", "leads", "lead", "signups", "sign ups",
		"installs", "install", "registrations", "add to cart", "adds to cart",
		"orders", "order", "actions", "action"}},
	// "landing page views" before the impressions group claims "views": it is a
	// post-click event, nearer a click than an impression, and putting it among
	// impressions also let it hijack CTR's denominator.
	{"clicks", true, 2, []string{"clicks", "click", "landing page views",
		"landing page view", "taps", "tap", "visits", "sessions"}},
	// Reach is deliberately NOT here. It counts deduplicated people, so it does not
	// add across campaigns -- audiences overlap -- and the account's true reach is
	// not derivable from the export at all. It was being summed and labelled an
	// impression: three campaigns reaching 600, 700 and 800 gave an account reach
	// of 2,100 when the truth might be 900. Rather than pick a wrong aggregation it
	// is left out of the allow-list, so it is stored and named on screen but not
	// forecast. Frequency, which is impressions over reach, is a rate and is below.
	{"impressions", true, 5, []string{"impressions", "impression", "impr", "imps",
		"imp", "views", "view", "plays"}},
	{"spend", true, 4, []string{"cost", "spend", "spent", "amount"}},
}

// normaliseColumn reduces a column name to lowercase words so a pattern can be
// matched on whole words. Everything that is decoration goes: a parenthesised
// qualifier ("Amount spent (USD)"), a trailing abbreviation dot ("Impr."),
// punctuation and repeated spaces.
//
// Whole words, never substrings, and this matters: a substring rule for "budget"
// would also claim "Budget name", and one for "imp" would claim "important".
func normaliseColumn(name string) string {
	n := foldAccents(strings.ToLower(strings.TrimSpace(withoutQualifier(name))))
	var b strings.Builder
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return " " + strings.Join(strings.Fields(b.String()), " ") + " "
}

// withoutQualifier drops a parenthesised suffix: "Amount spent (USD)" is spend,
// and "Purchases (web/app)" is a count of purchases, not a ratio.
func withoutQualifier(name string) string {
	for {
		i := strings.IndexByte(name, '(')
		if i < 0 {
			return name
		}
		j := strings.IndexByte(name[i:], ')')
		if j < 0 {
			return name[:i] // unbalanced: drop the rest rather than keep half of it
		}
		name = name[:i] + name[i+j+1:]
	}
}

// foldAccents reduces the Latin letters an export actually uses to ASCII, so a
// non-English header is matched on its letters rather than thrown away. Keeping
// only [a-z0-9] turned "Coût" into "co t", which matched nothing and silently
// dropped the column from the forecast.
func foldAccents(s string) string {
	const from = "àáâãäåèéêëìíîïòóôõöùúûüýÿñçšžœæðþ"
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 'œ':
			b.WriteString("oe")
		case r == 'æ':
			b.WriteString("ae")
		case strings.ContainsRune(from, r):
			switch {
			case strings.ContainsRune("àáâãäå", r):
				b.WriteRune('a')
			case strings.ContainsRune("èéêë", r):
				b.WriteRune('e')
			case strings.ContainsRune("ìíîï", r):
				b.WriteRune('i')
			case strings.ContainsRune("òóôõö", r):
				b.WriteRune('o')
			case strings.ContainsRune("ùúûü", r):
				b.WriteRune('u')
			case strings.ContainsRune("ýÿ", r):
				b.WriteRune('y')
			case r == 'ñ':
				b.WriteRune('n')
			case r == 'ç':
				b.WriteRune('c')
			case r == 'š':
				b.WriteRune('s')
			case r == 'ž':
				b.WriteRune('z')
			case r == 'ð':
				b.WriteRune('d')
			case r == 'þ':
				b.WriteString("th")
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// metricConcept says which of the wanted metrics a column is, if any. The second
// result is false when the column is a per-unit cost or a rate, which means it is
// averaged across campaigns rather than summed.
func metricConcept(name string) (string, bool, bool) {
	n := normaliseColumn(name)

	// "A / B" is a ratio however it is spelled, and Google Ads spells its two most
	// important ones that way: "Cost / conv." is CPA and "Conv. value / cost" is
	// ROAS. Normalising drops the slash, so "Cost / conv." became " cost conv ",
	// matched "conv" from the conversions group before "cost per" could fire, and
	// was **summed** across campaigns. Measured on a three-campaign day: CPA stored
	// as 74.96 against a true 31.78, and ROAS as 8.75 against a true 3.53 -- while
	// the same file's "ROAS" column, which does match the rate group, stored 2.92.
	// Two spellings of one metric, two wrong answers, on the page together.
	if bare := withoutQualifier(name); strings.Contains(bare, "/") {
		for _, part := range strings.Split(bare, "/") {
			for _, m := range wantedMetrics {
				for _, pat := range m.patterns {
					if strings.Contains(normaliseColumn(part), " "+pat+" ") {
						return "ratio", false, true
					}
				}
			}
		}
	}

	for _, m := range wantedMetrics {
		for _, pat := range m.patterns {
			if strings.Contains(n, " "+pat+" ") {
				return m.concept, m.addable, true
			}
		}
	}
	return "", true, false
}

// withConcepts lists the forecast columns, each with the wanted metric it matched.
// Saying "Amount spent (USD) (spend)" is what makes the allow-list auditable: the
// reader can see a column was understood, and which of their metrics it answers.
func withConcepts(d *Data, metrics []string) string {
	out := make([]string, len(metrics))
	for i, m := range metrics {
		if c := d.Concept[m]; c != "" && !strings.EqualFold(c, m) {
			out[i] = fmt.Sprintf("%s (%s)", m, c)
		} else {
			out[i] = m
		}
	}
	return strings.Join(out, ", ")
}

// entityLabels finds the readable name for each value of an ID group column, and
// reports any campaign whose name changed during the period.
//
// The name used is the one on the **last day the ID appears**, which is what the
// campaign is called now. Every earlier row for that ID is relabelled to it, so a
// rename leaves one continuous series rather than two half-length ones.
//
// Two IDs can end up wanting the same name -- a name reused after a campaign was
// deleted, or two campaigns genuinely named alike. Folding them together would
// silently add two campaigns' numbers into one series, so the ID is appended to
// both instead.
//
// Returns the label column's name, the map, and the renames found. All three are
// empty when no column can serve as a name.
func entityLabels(header, names []string, numeric []bool, rows []row,
	group string, rowsPerDay int) (string, map[string]string, []string) {

	groupCol := -1
	for c, n := range names {
		if n == group {
			groupCol = c
		}
	}
	if groupCol < 0 {
		return "", nil, nil
	}

	// Choosing the name column. It need not have exactly rowsPerDay distinct values
	// -- a rename is precisely the case where it has more. Nor need it be unique
	// within a day: two campaigns can genuinely share a name, and requiring
	// uniqueness there fell back to bare IDs for exactly the file that needed a
	// name most.
	//
	// What it must do is name one thing: no ID may carry two different values for it
	// on the same day. Among the columns that manage that, the one whose names tell
	// the most campaigns apart wins, and file order breaks a tie. That is what
	// separates "Campaign" (fifteen names) from "Currency code" (one), without
	// either being named in the code.
	nameCol, bestScore := -1, -1
	for c, n := range names {
		if c == groupCol || numeric[c] || looksLikeIdentifier(n) {
			continue
		}
		perDay := map[string]string{}
		last, latest := map[string]string{}, map[string]string{}
		ok := true
		for _, rw := range rows {
			if c+1 >= len(rw.raw) {
				ok = false
				break
			}
			id := strings.TrimSpace(rw.raw[groupCol+1])
			v := strings.TrimSpace(rw.raw[c+1])
			if v == "" {
				ok = false
				break
			}
			k := rw.day + "\x1f" + id
			if was, seen := perDay[k]; seen && was != v {
				ok = false // two names for one campaign on one day: not a name column
				break
			}
			perDay[k] = v
			if rw.day >= last[id] {
				last[id], latest[id] = rw.day, v
			}
		}
		if !ok {
			continue
		}
		distinct := map[string]bool{}
		for _, v := range latest {
			distinct[v] = true
		}
		if len(distinct) > bestScore {
			nameCol, bestScore = c, len(distinct)
		}
	}
	if nameCol < 0 {
		return "", nil, nil
	}
	best := nameCol

	lastDay, names_ := map[string]string{}, map[string]string{}
	first := map[string]string{}
	for _, rw := range rows {
		id := strings.TrimSpace(rw.raw[groupCol+1])
		nm := clean(strings.TrimSpace(rw.raw[best+1]))
		if _, ok := first[id]; !ok {
			first[id] = nm
		}
		if rw.day >= lastDay[id] {
			lastDay[id], names_[id] = rw.day, nm
		}
	}

	// Disambiguate names two IDs both claim.
	count := map[string]int{}
	for _, nm := range names_ {
		count[nm]++
	}
	label := map[string]string{}
	for id, nm := range names_ {
		if count[nm] > 1 {
			nm = fmt.Sprintf("%s (%s)", nm, id)
		}
		label[id] = nm
	}

	var renamed []string
	for id, was := range first {
		if now := names_[id]; now != was {
			renamed = append(renamed, fmt.Sprintf("%q -> %q", was, label[id]))
		}
	}
	sort.Strings(renamed)
	return names[best], label, renamed
}

// labelKey is the group value an entity is stored under -- the inverse of d.Label,
// used where a list was built from raw column values but is checked against the
// entity names those values became.
func labelKey(d *Data, entity string) string {
	for id, lbl := range d.Label {
		if lbl == entity {
			return id
		}
	}
	return entity
}

// rateDenominator names the concept a rate is "per", so the account figure can be
// the real blended one instead of a plain mean of the campaigns' rates.
//
// It is read off the column's own name, which already says it: "A / B" is per B,
// "cost per X" is per X, and the standard abbreviations each have a fixed
// denominator. When the file does not carry that column there is nothing to weight
// by and the mean stays unweighted.
func rateDenominator(name string) string {
	n := normaliseColumn(name)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		if c, _, ok := metricConceptPlain(name[i+1:]); ok {
			return c
		}
	}
	switch {
	case strings.Contains(n, " ctr "), strings.Contains(n, " click through rate "),
		strings.Contains(n, " cpm "), strings.Contains(n, " cpv "):
		return "impressions"
	case strings.Contains(n, " cpc "), strings.Contains(n, " cvr "),
		strings.Contains(n, " conversion rate "):
		return "clicks"
	case strings.Contains(n, " cpa "), strings.Contains(n, " cpl "):
		return "conversions"
	case strings.Contains(n, " roas "):
		return "spend"
	}
	if i := strings.Index(n, " per "); i >= 0 {
		if c, _, ok := metricConceptPlain(n[i+4:]); ok {
			return c
		}
	}
	return ""
}

// denominatorPhrase is the words a rate says it is "per": "Cost per add to cart"
// is per "add to cart". Empty for an abbreviation like CTR, which names no words.
func denominatorPhrase(name string) string {
	n := normaliseColumn(name)
	if i := strings.LastIndex(withoutQualifier(name), "/"); i >= 0 {
		return strings.TrimSpace(normaliseColumn(withoutQualifier(name)[i+1:]))
	}
	if i := strings.Index(n, " per "); i >= 0 {
		return strings.TrimSpace(n[i+4:])
	}
	return ""
}

// canonCount is how many leading patterns name the concept plainly.
func canonCount(concept string) int {
	for _, m := range wantedMetrics {
		if m.concept == concept {
			return m.canon
		}
	}
	return 0
}

// patternRank says how canonical a column's name is for a concept: the position
// of the pattern it matched in that concept's list, which is written
// most-canonical first. Lower is better. Unmatched sorts last.
func patternRank(name, concept string) int {
	n := normaliseColumn(name)
	for _, m := range wantedMetrics {
		if m.concept != concept {
			continue
		}
		for i, pat := range m.patterns {
			if strings.Contains(n, " "+pat+" ") {
				return i
			}
		}
	}
	return 1 << 29
}

// metricConceptPlain is metricConcept without the slash rule, for asking what one
// side of a ratio is. Going through metricConcept would answer "ratio" for every
// part of a name that still contains a slash.
func metricConceptPlain(name string) (string, bool, bool) {
	n := normaliseColumn(name)
	for _, m := range wantedMetrics {
		for _, pat := range m.patterns {
			if strings.Contains(n, " "+pat+" ") {
				return m.concept, m.addable, true
			}
		}
	}
	return "", true, false
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

// sameCampaignsEveryDay refuses a file where some day carries a different set of
// campaigns from the first, or the same one twice.
//
// It reports the first day that differs, and says which names are wrong and how,
// because "your export is inconsistent" sends someone scrolling through 16,000
// rows. Rows are already sorted by day, so the first difference found is the
// earliest one.
func sameCampaignsEveryDay(path string, rows []row, groupCol int, group string) error {
	name := func(rw row) string {
		n := strings.TrimSpace(rw.raw[groupCol+1])
		if n == "" {
			n = "(unnamed)"
		}
		return n
	}

	// The first day sets the expectation. Duplicates in it are caught here too.
	want := map[string]bool{}
	day0 := rows[0].day
	for _, rw := range rows {
		if rw.day != day0 {
			break
		}
		if want[name(rw)] {
			return fmt.Errorf("%s: %s lists %s %q twice. Adding both into one day "+
				"doubles it and hides whichever %s is missing. Re-export a clean range",
				path, day0, group, name(rw), group)
		}
		want[name(rw)] = true
	}

	seen := map[string]bool{}
	day := day0
	check := func(d string) error {
		if d == day0 {
			return nil
		}
		var missing []string
		for n := range want {
			if !seen[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		sort.Strings(missing)
		return fmt.Errorf("%s: %s is missing %s %s, which %s has. Every day must "+
			"carry the same %ss, or adding them up invents a jump in the totals. "+
			"Re-export a complete date range",
			path, day, group, listOr(missing, "none"), day0, group)
	}

	for _, rw := range rows {
		if rw.day != day {
			if err := check(day); err != nil {
				return err
			}
			day, seen = rw.day, map[string]bool{}
		}
		n := name(rw)
		if seen[n] {
			return fmt.Errorf("%s: %s lists %s %q twice. Adding both into one day "+
				"doubles it and hides whichever %s is missing. Re-export a clean range",
				path, rw.day, group, n, group)
		}
		if !want[n] {
			return fmt.Errorf("%s: %s has %s %q, which %s does not. Every day must "+
				"carry the same %ss, or adding them up invents a jump in the totals. "+
				"Re-export a complete date range",
				path, rw.day, group, n, day0, group)
		}
		seen[n] = true
	}
	return check(day)
}

// campaignStates are the words an ad platform writes in its status column, and
// whether each one means the campaign is switched on.
var campaignStates = map[string]bool{
	"enabled": true, "active": true, "running": true, "live": true, "serving": true,
	"paused": false, "removed": false, "ended": false, "disabled": false,
	"archived": false, "stopped": false, "deleted": false, "draft": false,
}

// statusColumn finds the column holding campaign state, by its values rather
// than its name.
//
// A Google Ads export calls it "Campaign status", but the file also carries a
// "Status" column holding serving states ("Eligible (Limited)") and a "Status
// reasons" column holding prose. Matching on the word "status" picks the wrong
// one. A column is believed only when every value it holds is a state in
// campaignStates, which those two fail and a campaign-name column fails too.
func statusColumn(d *Data) string {
	known, unknown := map[string]bool{}, map[string]bool{}
	for _, r := range d.Raw {
		for _, name := range d.Skipped { // text columns only
			v := strings.ToLower(r.Data[name])
			if v == "" {
				continue
			}
			if _, ok := campaignStates[v]; ok {
				known[name] = true
			} else {
				unknown[name] = true
			}
		}
	}
	for _, name := range d.Skipped { // file order, so the leftmost one wins
		if known[name] && !unknown[name] {
			return name
		}
	}
	return ""
}

// runningEntities returns the entities the export says are switched on as of its
// last day, or nil when the file carries no campaign-status column -- in which
// case the constant-series rule is the only one that applies.
//
// An entity with no row on the last day is not running: it is not in the current
// state of the account at all.
func runningEntities(d *Data) map[string]bool {
	if d.GroupBy == "" || len(d.Days) == 0 {
		return nil
	}
	col := statusColumn(d)
	if col == "" {
		return nil
	}
	last := d.Days[len(d.Days)-1]
	running := map[string]bool{}
	for _, r := range d.Raw {
		if r.Day == last && campaignStates[strings.ToLower(r.Data[col])] {
			// Keyed by the name the entity is stored under, which is the label when
			// the group column is an ID. Keying by the raw ID here would match
			// nothing against d.Entities and mark every campaign switched off.
			e := r.Data[d.GroupBy]
			if lbl, ok := d.Label[e]; ok {
				e = lbl
			}
			running[e] = true
		}
	}
	return running
}

// whyNotForecast explains why an entity that is in the file has no forecast.
func whyNotForecast(d *Data, e string) string {
	if slicesContainsFold(d.Paused, e) {
		return "the export says it is switched off, so its next few days are a " +
			"decision rather than a forecast"
	}
	if slicesContainsFold(d.Stopped, e) {
		return "the export stopped listing it before its last day, so it was not " +
			"running by then and its next few days are a decision rather than a forecast"
	}
	return "nothing it records moved during this period, so there is nothing to forecast for it"
}

// exclusionLines reports what was stored but not forecast, one line per reason.
func exclusionLines(d *Data) []string {
	var idle []string
	for _, e := range d.Inactive {
		if !slicesContainsFold(d.Paused, e) && !slicesContainsFold(d.Stopped, e) {
			idle = append(idle, e)
		}
	}
	var out []string
	if len(d.Paused) > 0 {
		out = append(out, "  switched off in the export, stored but not forecast: "+
			listSome(d.Paused))
	}
	if len(d.Stopped) > 0 {
		out = append(out, "  stopped running before the export's last day, stored but not forecast: "+
			listSome(d.Stopped))
	}
	if len(idle) > 0 {
		out = append(out, "  no activity at all, stored but not forecast: "+listSome(idle))
	}
	return out
}

// blendLines says, per rate, how the account figure was arrived at.
//
// The two answers differ by multiples and were printed identically. Measured on
// one file downloaded twice, once with an impressions column and once without:
// account CTR 0.5645 against 4.4633, from the same campaign CTRs, both announced
// as "blended". A reader comparing month to month sees a 7.9x change that is
// entirely an artefact of which columns were ticked in the download dialog.
func blendLines(d *Data, metrics []string) []string {
	var weighted, plain []string
	for _, n := range intersect(d.Averaged, metrics) {
		if w := d.WeightedBy[n]; w != "" {
			weighted = append(weighted, fmt.Sprintf("%s (by %s)", n, w))
		} else {
			plain = append(plain, n)
		}
	}
	var out []string
	if len(weighted) > 0 {
		out = append(out, "  account figure is the blended rate, weighted by the "+
			"column named: "+strings.Join(weighted, ", "))
	}
	if len(plain) > 0 {
		out = append(out, "  account figure is a plain mean of the campaigns that "+
			"reported -- this file has no column to weight by, so it is NOT the "+
			"blended rate: "+strings.Join(plain, ", "))
	}
	return out
}

// oneCurrency refuses an export that mixes them.
//
// A multi-market or MCC download carries a currency column, and every figure this
// tool derives -- the account total, a blended CPA, a ROAS, everything accuracy
// scores -- is arithmetic across the rows. Adding 100 USD to 300 EUR gives 400 of
// nothing. The column was read, classified as text and then ignored, so the sum
// was printed with no marker of any kind.
//
// Where there is exactly one currency it is kept, so the report can say which.
func oneCurrency(path string, d *Data) error {
	col := ""
	for _, n := range d.Skipped {
		if c := normaliseColumn(n); strings.Contains(c, " currency ") ||
			strings.Contains(c, " currency code ") {
			col = n
			break
		}
	}
	if col == "" {
		return nil
	}
	seen := map[string]bool{}
	var found []string
	for _, r := range d.Raw {
		v := strings.TrimSpace(r.Data[col])
		if v != "" && !seen[v] {
			seen[v] = true
			found = append(found, v)
		}
	}
	sort.Strings(found)
	if len(found) > 1 {
		return fmt.Errorf("%s mixes %d currencies (%s in %q). Every figure here is "+
			"arithmetic across the rows -- the account total, the blended rates -- and "+
			"adding two currencies together gives a number in neither. Export one "+
			"currency at a time",
			path, len(found), strings.Join(found, ", "), col)
	}
	if len(found) == 1 {
		d.Currency = found[0]
	}
	return nil
}

// listSome names the first few and counts the rest.
//
// The list is chosen by the file, so it is unbounded: a 20,000-campaign export put
// 308,935 bytes on a single terminal line, 99.4% of the whole run's output, which
// scrolls away every message worth reading -- including the "forecasting:" line
// the docs tell people to check when something is missing. The full list is in the
// database, which is where a list that long belongs.
func listSome(names []string) string {
	const show = 12
	if len(names) <= show {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:show], ", "), len(names)-show)
}

// intersect keeps the members of list that are also in keep, in list's order.
func intersect(list, keep []string) []string {
	var out []string
	for _, v := range list {
		if slicesContainsFold(keep, v) {
			out = append(out, v)
		}
	}
	return out
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
	if i < len(header) && clean(strings.TrimSpace(header[i])) != "" {
		return clean(strings.TrimSpace(header[i]))
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
