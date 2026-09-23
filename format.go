package main

// Number formatting for the terminal table and the HTML report.
//
// %.2f is right for money and wrong for everything else: a series around 1e-9
// prints as a column of 0.00, and an all-zero forecast prints "-0.00" because
// the models return negative zero. Pick the precision from the magnitude instead.

import (
	"math"
	"strconv"
)

// formatMetric formats a value, putting the percent sign back on a column that
// was written with one. The number is stored as written ("4.20%" -> 4.20), so
// this is only the sign, not a conversion.
func formatMetric(v float64, percent bool) string {
	s := formatValue(v)
	if percent {
		return s + "%"
	}
	return s
}

func formatValue(v float64) string {
	if v == 0 {
		return "0" // also normalises negative zero
	}
	switch a := math.Abs(v); {
	case a >= 1e15 || a < 1e-4:
		return strconv.FormatFloat(v, 'g', 6, 64) // scientific, rather than a row of zeros
	case a < 1:
		return strconv.FormatFloat(v, 'f', 6, 64)
	case a < 100:
		return strconv.FormatFloat(v, 'f', 3, 64)
	default:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
}
