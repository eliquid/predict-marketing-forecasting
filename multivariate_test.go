package main

// Every metric must get its own real forecast.
//
// The failure this guards against is subtle and would look fine: forecasting one
// metric and deriving the others from it by a ratio. The numbers would be
// plausible, the charts would look right, and the extra metrics would be fiction.
//
// Three metrics with deliberately unrelated shapes are forecast together. A
// derived metric could not follow a shape its source does not have.

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func shapesCSV(invertWave bool) string {
	var b strings.Builder
	b.WriteString("date,ramp,flat,wave\n")
	for i := 0; i < 140; i++ {
		w := 800 + 250*math.Sin(2*math.Pi*float64(i)/7)
		if invertWave {
			w = 800 - 250*math.Sin(2*math.Pi*float64(i)/7)
		}
		fmt.Fprintf(&b, "%s,%d,500,%.4f\n", day(i), 100+i*6, w)
	}
	return b.String()
}

func forecastShapes(t *testing.T, model, body string) map[string][]float64 {
	t.Helper()
	w, err := startWorker(model)
	if err != nil {
		t.Skipf("%s unavailable: %v", model, err)
	}
	defer w.Close()

	d, err := readCSV(writeTemp(t, "s.csv", body), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	values := make([][]float64, len(d.Names))
	for i, n := range d.Names {
		values[i] = d.Values[AccountEntity][n]
	}
	q, err := w.Forecast(values, d.Names, 7, w.Shake.Quantiles, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mid := len(w.Shake.Quantiles) / 2
	out := map[string][]float64{}
	for mi, n := range d.Names {
		for _, day := range q[mi] {
			out[n] = append(out[n], day[mid])
		}
	}
	return out
}

func TestEachMetricGetsItsOwnForecast(t *testing.T) {
	for _, model := range []string{"timesfm3", "chronos2"} {
		t.Run(model, func(t *testing.T) {
			med := forecastShapes(t, model, shapesCSV(false))

			// A constant history must stay constant, not inherit the ramp's slope.
			lo, hi := med["flat"][0], med["flat"][0]
			for _, v := range med["flat"] {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
			if hi-lo > 1 {
				t.Errorf("flat varies by %.3f; it should stay at 500", hi-lo)
			}
			if math.Abs(med["flat"][0]-500) > 5 {
				t.Errorf("flat forecast %.2f, want about 500", med["flat"][0])
			}

			// The ramp must keep rising.
			for i := 1; i < len(med["ramp"]); i++ {
				if med["ramp"][i] <= med["ramp"][i-1] {
					t.Errorf("ramp stopped rising at day %d: %v", i, med["ramp"])
					break
				}
			}

			// The wave must turn downwards at some point. A metric derived from the
			// monotonic ramp could never do that.
			downs := 0
			for i := 1; i < len(med["wave"]); i++ {
				if med["wave"][i] < med["wave"][i-1] {
					downs++
				}
			}
			if downs == 0 {
				t.Errorf("wave never falls, so it is not following its own cycle: %v", med["wave"])
			}

			// If wave were ramp times a constant, this ratio would not move.
			rlo, rhi := math.Inf(1), math.Inf(-1)
			for i := range med["wave"] {
				r := med["wave"][i] / med["ramp"][i]
				rlo, rhi = math.Min(rlo, r), math.Max(rhi, r)
			}
			if rhi-rlo < 0.05 {
				t.Errorf("wave/ramp ratio is nearly constant (%.4f..%.4f): wave looks derived",
					rlo, rhi)
			}
		})
	}
}

// Changing one metric's history must change that metric's forecast and leave the
// others alone. A derived metric would not react to its own data at all.
func TestOneMetricsHistoryOnlyMovesThatMetric(t *testing.T) {
	model := "timesfm3"
	a := forecastShapes(t, model, shapesCSV(false))
	b := forecastShapes(t, model, shapesCSV(true)) // only 'wave' differs

	for _, unchanged := range []string{"ramp", "flat"} {
		for i := range a[unchanged] {
			if math.Abs(a[unchanged][i]-b[unchanged][i]) > 1e-6 {
				t.Errorf("%s moved when only 'wave' changed: %v vs %v",
					unchanged, a[unchanged], b[unchanged])
				break
			}
		}
	}
	moved := 0.0
	for i := range a["wave"] {
		moved = math.Max(moved, math.Abs(a["wave"][i]-b["wave"][i]))
	}
	if moved < 50 {
		t.Errorf("inverting the wave's own history barely moved its forecast (%.2f); "+
			"it is not being forecast from its own data", moved)
	}
}
