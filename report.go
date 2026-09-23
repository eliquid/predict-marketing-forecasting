package main

// The HTML report: one self-contained file you double-click.
//
// The chart is inline SVG drawn here, so the page needs no chart library, no
// JavaScript and no network. htmx is included because the project calls for it;
// see README.md for what it can and cannot do from a file:// page.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"strings"
	"time"
)

type reportData struct {
	Run       Run
	Generated string
	// Account may be nil: -entities can pick campaigns without the total.
	Account     *entityReport
	Campaigns   []entityReport // one per campaign, collapsed by default
	MetricNames []string       // column headings for the summary table
	// Excluded names campaigns that were in the file but had no activity in the
	// period, so there was nothing to forecast. The terminal says so; the report
	// is the copy people actually pass around, and a campaign vanishing from it
	// with no explanation reads like a bug or an omission.
	Excluded []string
	Model    Handshake
	HTMX     template.JS // inlined, so the report is a single portable file
}

// One block per thing forecast: the account, or one campaign.
type entityReport struct {
	Name    string
	Metrics []metricReport
	Totals  []entityTotal // next-horizon totals, for the summary line
}

type entityTotal struct {
	Metric string
	Value  string
}

// One block per forecast metric: its chart and its numbers.
type metricReport struct {
	Name  string
	Chart template.HTML
	Rows  []reportRow
}

type reportRow struct {
	Day               string
	Low, Median, High string // pre-formatted; see format.go
}

// writeReport writes the whole run as one self-contained page: every forecast
// metric gets its own chart and table. values is [metric][day][quantile].
// writeReport writes the whole run as one self-contained page: the account total
// first, then each campaign. values is entity -> [metric][day][quantile].
func writeReport(path string, run Run, data *Data, days []string,
	quantiles []float64, values map[string][][][]float64, historyDays int) error {

	var hs Handshake
	if len(run.ModelInfo) > 0 {
		_ = json.Unmarshal(run.ModelInfo, &hs)
	}
	mid := len(quantiles) / 2

	build := func(entity string) entityReport {
		er := entityReport{Name: entity}
		for mi, name := range run.Metrics {
			per := values[entity][mi]
			rows := make([]reportRow, len(days))
			total := 0.0
			pct := data.Percent[name]
			for i, day := range days {
				rows[i] = reportRow{Day: day,
					Low:    formatMetric(per[i][0], pct),
					Median: formatMetric(per[i][mid], pct),
					High:   formatMetric(per[i][len(per[i])-1], pct),
				}
				total += per[i][mid]
			}
			er.Metrics = append(er.Metrics, metricReport{
				Name:  name,
				Chart: drawChart(data.Series(entity, name), days, per, mid, historyDays),
				Rows:  rows,
			})
			// A rate does not add up over days; show its average instead.
			if pct || data.Averaged != nil && slicesContainsFold(data.Averaged, name) {
				er.Totals = append(er.Totals, entityTotal{Metric: name + " (avg)",
					Value: formatMetric(total/float64(len(days)), pct)})
			} else {
				er.Totals = append(er.Totals, entityTotal{Metric: name,
					Value: formatValue(total)})
			}
		}
		return er
	}

	d := reportData{
		Run: run, Generated: time.Now().Format("2006-01-02 15:04"),
		Model: hs, HTMX: template.JS(htmxJS), Excluded: data.Inactive,
	}
	// Header labels come from the metrics themselves, not from the account, which
	// may not have been asked for.
	for _, name := range run.Metrics {
		label := name
		if data.Percent[name] || slicesContainsFold(data.Averaged, name) {
			label += " (avg)"
		}
		d.MetricNames = append(d.MetricNames, label)
	}
	for _, e := range run.Entities {
		er := build(e)
		if e == AccountEntity {
			acc := er
			d.Account = &acc
			continue
		}
		d.Campaigns = append(d.Campaigns, er)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// The page names real campaigns and what they spend, the same data the
	// database is kept at 0600 for. os.Create would leave it world-readable under
	// the usual umask. Sharing it stays a deliberate act -- copy it, attach it,
	// serve it -- rather than something the file mode does for you.
	if err := f.Chmod(0o600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("securing %s: %w", path, err)
	}
	return reportTmpl.Execute(f, d)
}

// drawChart renders history plus the forecast band as inline SVG.
//
// Only the last `show` days of history are drawn (default 90): more than that and
// the forecast -- the part you are actually looking at -- becomes a few pixels wide.
func drawChart(history []Point, days []string, values [][]float64, mid, show int) template.HTML {
	const w, h, padL, padR, padT, padB = 900.0, 320.0, 60.0, 20.0, 20.0, 40.0

	if show <= 0 {
		show = 90
	}
	if len(history) > show {
		history = history[len(history)-show:]
	}
	n := len(history) + len(days)
	if n < 2 {
		return ""
	}

	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range history {
		lo, hi = math.Min(lo, p.Value), math.Max(hi, p.Value)
	}
	for _, v := range values {
		lo, hi = math.Min(lo, v[0]), math.Max(hi, v[len(v)-1])
	}
	if hi <= lo {
		hi = lo + 1
	}
	pad := (hi - lo) * 0.08
	lo, hi = lo-pad, hi+pad

	x := func(i int) float64 { return padL + (w-padL-padR)*float64(i)/float64(n-1) }
	y := func(v float64) float64 { return padT + (h-padT-padB)*(1-(v-lo)/(hi-lo)) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="chart" role="img" aria-label="forecast chart">`, w, h)

	// horizontal grid + y labels
	for i := 0; i <= 4; i++ {
		v := lo + (hi-lo)*float64(i)/4
		yy := y(v)
		fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" class="grid"/>`, padL, yy, w-padR, yy)
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="ylab">%s</text>`, padL-8, yy+4, compact(v))
	}

	// forecast band, q10..q90
	var top, bot strings.Builder
	for i, v := range values {
		xi := x(len(history) + i)
		fmt.Fprintf(&top, "%g,%g ", xi, y(v[len(v)-1]))
		fmt.Fprintf(&bot, "%g,%g ", xi, y(v[0]))
	}
	fmt.Fprintf(&b, `<polygon class="band" points="%s%s"/>`, top.String(), reverse(bot.String()))

	// history line
	b.WriteString(`<polyline class="hist" points="`)
	for i, p := range history {
		fmt.Fprintf(&b, "%g,%g ", x(i), y(p.Value))
	}
	b.WriteString(`"/>`)

	// median forecast, joined to the last real observation
	b.WriteString(`<polyline class="fc" points="`)
	if len(history) > 0 {
		fmt.Fprintf(&b, "%g,%g ", x(len(history)-1), y(history[len(history)-1].Value))
	}
	for i, v := range values {
		fmt.Fprintf(&b, "%g,%g ", x(len(history)+i), y(v[mid]))
	}
	b.WriteString(`"/>`)

	// Divider between observed and forecast. Only the span ends are labelled --
	// the divider's own date sits close to the right-hand label and the two
	// collide whenever the forecast is short next to the history.
	if len(history) > 0 {
		xd := x(len(history) - 1)
		fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" class="split"/>`, xd, padT, xd, h-padB)
		fmt.Fprintf(&b, `<text x="%g" y="%g" class="xlab">%s</text>`, padL, h-padB+16, history[0].Day)
	}
	fmt.Fprintf(&b, `<text x="%g" y="%g" class="xlab end">%s</text>`, w-padR, h-padB+16, days[len(days)-1])

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func reverse(points string) string {
	f := strings.Fields(points)
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
	return strings.Join(f, " ")
}

func compact(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}
