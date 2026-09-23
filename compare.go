package main

// The comparison report: several models on one page, one chart at a time.
//
// The single-model report in report.go answers "what does this model say". This
// one answers "do the models agree", which is the question you actually have
// once more than one of them has run. Every model's median is drawn on the same
// axes against the same history, so disagreement is visible rather than
// something you reconstruct by flipping between two files.
//
// A run of a real export is 40 campaigns by 7 metrics, which is 280 charts. They
// are all drawn into the page and all but one hidden, and two dropdowns choose
// which is shown. That keeps the page a single file that works from file:// with
// no server, which is the whole point of the report.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// modelLine is one model's forecast of one series, ready to draw.
type modelLine struct {
	Model  string    // "chronos2"
	Colour string    // stroke, assigned per model
	Median []float64 // one value per forecast day
}

// statCard is one figure above the chart: the history total, then what each
// model expects over the horizon. They live inside the pane so they follow the
// dropdowns instead of describing a view nobody is looking at.
type statCard struct {
	Caption string
	Value   string
	Note    string
	Lead    bool // the first model, drawn with the accent border
}

// comparison is everything the page needs.
type comparison struct {
	SeriesID  string
	Generated string
	AsOf      string // last day of real data every model was given
	Horizon   int
	Models    []comparedModel
	Entities  []string // dropdown 1, account first
	Metrics   []string // dropdown 2
	Panes     []chartPane
	Excluded  []string
	HTMX      template.JS
}

// comparedModel is a row in the provenance table, so the page can still say
// exactly what produced every line on it.
type comparedModel struct {
	Name            string
	Colour          string
	Repo            string
	Revision        string
	Weights         string
	Stroke          string // css border-top-style, matching the line's dashes
	TrainedThrough  string // chronos2ft only: the last day it was trained on
	IsFineTuned     bool
	QuantileCount   int
	VersionsSummary string
}

// chartPane is one (entity, metric) view: the chart plus the numbers under it.
type chartPane struct {
	Entity string
	Metric string
	Chart  template.HTML
	Totals []statCard
	Rows   []compareRow
	Header []string // "day" then one column per model
}

type compareRow struct {
	Day    string
	Values []string // one per model, same order as Header[1:]
}

// Colours are assigned by position, not by model name, so a fourth model gets a
// colour without anyone editing a map.
var lineColours = []string{"#4a9eff", "#e8a33d", "#3fb950", "#db6ec4", "#56d4dd"}

func colourFor(i int) string { return lineColours[i%len(lineColours)] }

// forecastRun is one model's stored answer, as writeComparison needs it.
type forecastRun struct {
	Run    Run
	Shake  Handshake
	Values map[string][][][]float64 // entity -> [metric][day][quantile]
}

// writeComparison renders several models' forecasts of the same data as one page.
//
// Every run must have been made from the same file, horizon and as_of; that is
// the caller's job, and cmdImport does it by running them back to back on one
// Data. Mixing runs from different days would put lines on the same axes that
// were never comparable.
func writeComparison(path string, runs []forecastRun, data *Data, days []string,
	historyDays int) error {

	if len(runs) == 0 {
		return fmt.Errorf("no forecasts to compare")
	}
	first := runs[0].Run

	c := comparison{
		SeriesID:  first.SeriesID,
		Generated: time.Now().Format("2006-01-02 15:04"),
		AsOf:      first.AsOf,
		Horizon:   first.Horizon,
		Excluded:  data.Inactive,
		HTMX:      template.JS(htmxJS),
	}

	for i, r := range runs {
		m := comparedModel{
			Name:          r.Run.Model,
			Colour:        colourFor(i),
			Repo:          r.Shake.Repo,
			Revision:      short(r.Shake.Revision),
			Weights:       short(r.Shake.WeightsSHA256),
			Stroke:        strokeFor(i),
			QuantileCount: len(r.Shake.Quantiles),
		}
		if t := trainedThrough(r.Run.ModelInfo); t != "" {
			m.TrainedThrough, m.IsFineTuned = t, true
		}
		var vs []string
		for k, v := range r.Shake.Versions {
			vs = append(vs, k+" "+v)
		}
		sort.Strings(vs)
		m.VersionsSummary = strings.Join(vs, ", ")
		c.Models = append(c.Models, m)
	}

	// Entities and metrics come from the runs, intersected so a dropdown never
	// offers a combination some model cannot draw.
	c.Entities = sharedEntities(runs)
	c.Metrics = sharedMetrics(runs)
	if len(c.Entities) == 0 || len(c.Metrics) == 0 {
		return fmt.Errorf("the runs have no entity or metric in common")
	}

	for _, entity := range c.Entities {
		for _, metric := range c.Metrics {
			pane := chartPane{Entity: entity, Metric: metric, Header: []string{"day"}}
			var lines []modelLine
			for i, r := range runs {
				mi := indexOf(r.Run.Metrics, metric)
				per, ok := r.Values[entity]
				if !ok || mi < 0 || mi >= len(per) {
					continue
				}
				q := per[mi]
				med := make([]float64, len(q))
				half := len(q[0]) / 2
				for d := range q {
					med[d] = q[d][half]
				}
				lines = append(lines, modelLine{
					Model: r.Run.Model, Colour: colourFor(i), Median: med,
				})
				pane.Header = append(pane.Header, r.Run.Model)
			}
			if len(lines) == 0 {
				continue
			}
			pct := data.Percent[metric]
			pane.Chart = drawCompareChart(data.Series(entity, metric), days, lines, historyDays)

			// What actually happened over the window the chart draws, then what
			// each model expects over the days ahead, on the same footing.
			hist := data.Series(entity, metric)
			window := historyDays
			if window <= 0 || window > len(hist) {
				window = len(hist)
			}
			observed := 0.0
			for _, pt := range hist[len(hist)-window:] {
				observed += pt.Value
			}
			pane.Totals = append(pane.Totals, statCard{
				Caption: fmt.Sprintf("observed · last %d days", window),
				Value:   formatMetric(observed, pct),
				Note:    "what actually happened",
			})
			for li, ln := range lines {
				sum := 0.0
				for _, v := range ln.Median {
					sum += v
				}
				pane.Totals = append(pane.Totals, statCard{
					Caption: fmt.Sprintf("next %d days · %s", len(days), ln.Model),
					Value:   formatMetric(sum, pct),
					Note:    "median, summed",
					Lead:    li == 0,
				})
			}
			for d, day := range days {
				row := compareRow{Day: day}
				for _, ln := range lines {
					if d < len(ln.Median) {
						row.Values = append(row.Values, formatMetric(ln.Median[d], pct))
					} else {
						row.Values = append(row.Values, "")
					}
				}
				pane.Rows = append(pane.Rows, row)
			}
			c.Panes = append(c.Panes, pane)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Same reasoning as the single-model report: the page names real campaigns
	// and what they spend, so it is created private rather than world-readable.
	if err := f.Chmod(0o600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("securing %s: %w", path, err)
	}
	return compareTmpl.Execute(f, c)
}

// trainedThrough pulls the fine-tuned cutoff out of a handshake, if there is one.
func trainedThrough(info []byte) string {
	if len(info) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(info, &m); err != nil {
		return ""
	}
	if s, ok := m["trained_through"].(string); ok {
		return s
	}
	return ""
}

// sharedEntities returns the entities every run forecast, account first so the
// page opens on the total rather than whichever campaign sorted first.
func sharedEntities(runs []forecastRun) []string {
	counts := map[string]int{}
	var order []string
	for _, r := range runs {
		seen := map[string]bool{}
		for _, e := range r.Run.Entities {
			if seen[e] {
				continue
			}
			seen[e] = true
			if counts[e] == 0 {
				order = append(order, e)
			}
			counts[e]++
		}
	}
	var out []string
	for _, e := range order {
		if counts[e] == len(runs) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i] == AccountEntity && out[j] != AccountEntity
	})
	return out
}

func sharedMetrics(runs []forecastRun) []string {
	counts := map[string]int{}
	var order []string
	for _, r := range runs {
		for _, m := range r.Run.Metrics {
			if counts[m] == 0 {
				order = append(order, m)
			}
			counts[m]++
		}
	}
	var out []string
	for _, m := range order {
		if counts[m] == len(runs) {
			out = append(out, m)
		}
	}
	return out
}

// drawCompareChart draws the history once and every model's median over it.
//
// Laid out like a trading chart rather than a textbook one: value labels on the
// right where the lines end, the divider between observed and forecast called
// out in the plot, and each line named where it finishes instead of only in a
// legend. With three models a legend alone means counting colours back and
// forth; a label at the end of the line does not.
//
// No uncertainty bands: three overlapping translucent bands turn the plot to
// mud, and the question this chart answers is whether the models agree, which
// the lines show directly.
func drawCompareChart(history []Point, days []string, lines []modelLine, show int) template.HTML {
	const w, h = 1000.0, 380.0
	const padL, padR, padT, padB = 16.0, 104.0, 26.0, 46.0

	if show <= 0 {
		show = 90
	}
	if len(history) > show {
		history = history[len(history)-show:]
	}
	n := len(history) + len(days)
	if n < 2 || len(lines) == 0 {
		return ""
	}

	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range history {
		lo, hi = math.Min(lo, p.Value), math.Max(hi, p.Value)
	}
	for _, ln := range lines {
		for _, v := range ln.Median {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if math.IsInf(lo, 0) || math.IsInf(hi, 0) {
		return ""
	}
	if hi <= lo {
		hi = lo + 1
	}
	// Start the scale at zero when the data is all positive: a spend chart that
	// does not is a chart that exaggerates every wobble.
	if lo > 0 && lo < hi*0.6 {
		lo = 0
	}
	pad := (hi - lo) * 0.10
	hi += pad
	if lo != 0 {
		lo -= pad
	}

	x := func(i int) float64 { return padL + (w-padL-padR)*float64(i)/float64(n-1) }
	y := func(v float64) float64 { return padT + (h-padT-padB)*(1-(v-lo)/(hi-lo)) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="chart" role="img" aria-label="forecast comparison">`, w, h)

	// gridlines, with the value written at the right-hand end of each
	for i := 0; i <= 4; i++ {
		v := lo + (hi-lo)*float64(i)/4
		yy := y(v)
		fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" class="grid"/>`, padL, yy, w-padR, yy)
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="ylab">%s</text>`, w-padR+12, yy+4, compact(v))
	}

	// day numbers along the bottom, thinned so they never collide
	step := 1
	for n/step > 14 {
		step++
	}
	for i := 0; i < n; i += step {
		var label string
		if i < len(history) {
			label = dayNumber(history[i].Day)
		} else {
			label = dayNumber(days[i-len(history)])
		}
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="xlab">%s</text>`, x(i), h-padB+22, label)
	}

	// what actually happened
	b.WriteString(`<polyline class="hist" points="`)
	for i, p := range history {
		fmt.Fprintf(&b, "%g,%g ", x(i), y(p.Value))
	}
	b.WriteString(`"/>`)
	if len(history) > 2 {
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="inline-label hist-label">actual</text>`,
			x(len(history)/2), y(hi)+16)
	}

	// the divider, and what it means
	if len(history) > 0 {
		xd := x(len(history) - 1)
		fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" class="split"/>`, xd, padT-6, xd, h-padB)
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="split-label">last day of data</text>`, xd+8, padT+6)
	}

	// one line per model, joined to the last real observation so the eye follows
	// it out of the history rather than starting it in mid-air
	var ends []lineEnd
	for li, ln := range lines {
		dash := ""
		if li > 0 {
			dash = fmt.Sprintf(` stroke-dasharray="%s"`, dashFor(li))
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2.5" `+
			`stroke-linejoin="round" stroke-linecap="round"%s points="`, ln.Colour, dash)
		if len(history) > 0 {
			fmt.Fprintf(&b, "%g,%g ", x(len(history)-1), y(history[len(history)-1].Value))
		}
		for i, v := range ln.Median {
			fmt.Fprintf(&b, "%g,%g ", x(len(history)+i), y(v))
		}
		b.WriteString(`"/>`)

		if len(ln.Median) > 0 {
			ex, ey := x(n-1), y(ln.Median[len(ln.Median)-1])
			fmt.Fprintf(&b, `<circle cx="%g" cy="%g" r="4" fill="%s"/>`, ex, ey, ln.Colour)
			ends = append(ends, lineEnd{x: ex, y: ey, colour: ln.Colour, name: ln.Model})
		}
	}

	// Models that finish close together would print their names on top of each
	// other, which is worst exactly when it matters most -- when they agree.
	for _, e := range spreadLabels(ends, 15, padT, h-padB) {
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="inline-label" fill="%s">%s</text>`,
			e.x-9, e.labelY, e.colour, template.HTMLEscapeString(e.name))
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// dashFor gives each model after the first its own dash pattern, so the lines
// are still tellable apart in print, in greyscale, or to a colour-blind reader.
func dashFor(i int) string {
	patterns := []string{"", "9 5", "2 4", "12 4 2 4"}
	return patterns[i%len(patterns)]
}

// dayNumber is the day-of-month from a YYYY-MM-DD date, which is what fits under
// a dense axis.
func dayNumber(day string) string {
	if len(day) >= 10 {
		d := strings.TrimPrefix(day[8:10], "0")
		return d
	}
	return day
}

// indexOf returns the position of s in list, or -1.
func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// strokeFor mirrors dashFor in CSS terms, so the legend swatch and the line it
// stands for are drawn the same way.
func strokeFor(i int) string {
	if i == 0 {
		return "solid"
	}
	return "dashed"
}

// lineEnd is where a model's line finishes, and where its name should go.
type lineEnd struct {
	x, y   float64
	labelY float64
	colour string
	name   string
}

// spreadLabels pushes overlapping end-labels apart, keeping their order and
// staying inside the plot. Two models that agree finish at nearly the same
// height, so without this their names are illegible precisely when the chart is
// telling you something useful.
func spreadLabels(ends []lineEnd, minGap, top, bottom float64) []lineEnd {
	if len(ends) == 0 {
		return ends
	}
	out := append([]lineEnd(nil), ends...)
	for i := range out {
		out[i].labelY = out[i].y - 10
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].labelY < out[j].labelY })

	// push down through the list, then clamp back up from the bottom
	for i := 1; i < len(out); i++ {
		if gap := out[i].labelY - out[i-1].labelY; gap < minGap {
			out[i].labelY = out[i-1].labelY + minGap
		}
	}
	if last := &out[len(out)-1]; last.labelY > bottom {
		shift := last.labelY - bottom
		for i := range out {
			out[i].labelY -= shift
		}
	}
	if out[0].labelY < top {
		shift := top - out[0].labelY
		for i := range out {
			out[i].labelY += shift
		}
	}
	return out
}
