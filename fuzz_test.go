package main

// Round 15: randomized input. The contract is narrow -- every entry point must
// either succeed or return an error. Never panic, never hang.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var csvFragments = []string{
	"date,v", "2026-01-01,100", "", ",", ",,,", "\"", "\"\"\"", "\xef\xbb\xbf",
	"2026-13-45,1", "not-a-date,1", "2026-01-01,", "2026-01-01,abc",
	"2026-01-01,1,2,3", "2026-01-01,\"1,000\"", "01/02/2026,5", "2026-01-01,-0",
	"2026-01-01,1e309", "2026-01-01,NaN", "2026-01-01,Inf", "\x00,1",
	"2026-01-01,£5", "2026-01-01,(5)", "2026-01-01,0x10", strings.Repeat("x", 500),
	"2026-02-29,1", "2028-02-29,1", "2026-01-01,1\r", "\t2026-01-01\t,\t1\t",
}

// Every generated file must produce either a Data or an error, and any Data must
// satisfy the invariants the rest of the program relies on.
func TestFuzzReadCSV(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	dir := t.TempDir()
	path := filepath.Join(dir, "f.csv")

	for i := 0; i < 3000; i++ {
		n := rng.Intn(40)
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteString(csvFragments[rng.Intn(len(csvFragments))])
			b.WriteByte('\n')
		}
		body := b.String()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		d, err := readCSV(path, nil, "") // must not panic
		if err != nil {
			continue
		}
		if len(d.Days) < smallestUsefulSeries {
			t.Fatalf("accepted %d rows, below the minimum\ninput:\n%s", len(d.Days), body)
		}
		for k := 1; k < len(d.Days); k++ {
			if d.Days[k-1] >= d.Days[k] {
				t.Fatalf("accepted unsorted days %s then %s\ninput:\n%s",
					d.Days[k-1], d.Days[k], body)
			}
		}
		if len(d.Names) == 0 {
			t.Fatalf("accepted a file with no numeric column\ninput:\n%s", body)
		}
		for name, col := range d.Values[AccountEntity] {
			if len(col) != len(d.Days) {
				t.Fatalf("column %q has %d values, series has %d", name, len(col), len(d.Days))
			}
			for _, v := range col {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("accepted a non-finite value %v\ninput:\n%s", v, body)
				}
			}
		}
	}
}

// Random fragments almost never assemble into a file long enough to reach value
// parsing: they repeat dates and fail the length and ordering checks first. So
// the invariants above went untested for three rounds, and "NaN" and "Inf" sat in
// the corpus looking like coverage while proving nothing -- readCSV was in fact
// storing both. This starts from a file that does load and corrupts one cell, so
// the deeper checks are actually reached.
func TestFuzzOneBadCell(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	path := filepath.Join(t.TempDir(), "m.csv")
	cells := append([]string{}, csvFragments...)
	cells = append(cells, "NaN", "Inf", "-Infinity", "1e400", "0/0", "--5", "1 000",
		"%", "$", "()", "1.2.3", "true", "null", "1,5%")

	for i := 0; i < 2000; i++ {
		rows := smallestUsefulSeries + rng.Intn(12)
		day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		bad := rng.Intn(rows)
		col := rng.Intn(2)

		var b strings.Builder
		b.WriteString("Day,Cost,Clicks\n")
		for r := 0; r < rows; r++ {
			v := []string{"100", "5"}
			if r == bad {
				v[col] = cells[rng.Intn(len(cells))]
			}
			fmt.Fprintf(&b, "%s,%s,%s\n",
				day.AddDate(0, 0, r).Format("2006-01-02"), v[0], v[1])
		}
		body := b.String()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		d, err := readCSV(path, nil, "")
		if err != nil {
			continue
		}
		for name, col := range d.Values[AccountEntity] {
			for _, v := range col {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("accepted a non-finite value %v in %q\ninput:\n%s", v, name, body)
				}
			}
			if len(col) != len(d.Days) {
				t.Fatalf("column %q has %d values, series has %d", name, len(col), len(d.Days))
			}
		}
	}
}

// checkForecast both validates and repairs, so it gets the same treatment.
func TestFuzzCheckForecast(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	odd := []float64{0, -0, 1, -1, 1e308, -1e308, math.NaN(), math.Inf(1), math.Inf(-1), 1e-308}

	for i := 0; i < 5000; i++ {
		days, nq := rng.Intn(5), rng.Intn(5)
		q := make([][]float64, days)
		for d := range q {
			q[d] = make([]float64, nq)
			for j := range q[d] {
				if rng.Intn(4) == 0 {
					q[d][j] = odd[rng.Intn(len(odd))]
				} else {
					q[d][j] = rng.NormFloat64() * 1000
				}
			}
		}
		_, err := checkForecast(q, days, nq) // must not panic
		if err != nil {
			continue
		}
		// Accepted output must be usable: ascending and finite.
		for _, day := range q {
			for j, v := range day {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("accepted non-finite %v in %v", v, day)
				}
				if j > 0 && v < day[j-1] {
					t.Fatalf("accepted unsorted quantiles %v", day)
				}
			}
		}
	}
}

func TestFuzzParseFuture(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	bits := []string{"a", "=", ",", ";", "1", "-1", "1e5", "nan", " ", "", "a=1", "a=1,2",
		strings.Repeat("9", 400), "=", "a=b", "\x00"}

	for i := 0; i < 5000; i++ {
		var b strings.Builder
		for j := 0; j < rng.Intn(8); j++ {
			b.WriteString(bits[rng.Intn(len(bits))])
		}
		horizon := rng.Intn(5)
		got, err := parseFuture(b.String(), horizon) // must not panic
		if err != nil {
			continue
		}
		for name, vals := range got {
			if len(vals) != horizon {
				t.Fatalf("accepted %q with %d values for horizon %d (input %q)",
					name, len(vals), horizon, b.String())
			}
		}
	}
}

func TestFuzzFormatValue(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 20000; i++ {
		v := math.Float64frombits(rng.Uint64())
		s := formatValue(v) // must not panic
		if s == "" {
			t.Fatalf("formatValue(%v) returned empty", v)
		}
		if strings.Contains(s, "-0") && v == 0 {
			t.Fatalf("negative zero leaked: %q", s)
		}
	}
	_ = fmt.Sprint()
}

// Coverage-guided fuzzing: Go mutates inputs toward unexplored code paths, which
// reaches shapes the table-driven fuzzer above will not think of.
//
//	go test -fuzz FuzzReadCSV -fuzztime 60s
func FuzzReadCSV(f *testing.F) {
	f.Add("date,v\n2026-01-01,1\n")
	f.Add("\xef\xbb\xbf\"date\",\"v\"\r\n2026-01-01,\"1,000\"\r\n")
	f.Add("01/02/2026,5\n02/01/2026,6\n")
	f.Add("2026-01-01,1,2\n2026-01-02,3,4\n")

	f.Fuzz(func(t *testing.T, body string) {
		path := filepath.Join(t.TempDir(), "f.csv")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Skip()
		}
		d, err := readCSV(path, nil, "")
		if err != nil {
			return
		}
		if len(d.Days) < smallestUsefulSeries {
			t.Fatalf("accepted %d rows", len(d.Days))
		}
		for i := 1; i < len(d.Days); i++ {
			if d.Days[i-1] >= d.Days[i] {
				t.Fatalf("unsorted: %s then %s", d.Days[i-1], d.Days[i])
			}
		}
		for name, col := range d.Values[AccountEntity] {
			if len(col) != len(d.Days) {
				t.Fatalf("column %q length %d vs %d", name, len(col), len(d.Days))
			}
		}
	})
}
