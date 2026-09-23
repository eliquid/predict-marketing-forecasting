package main

// Round 7: more than one thing happening at once.

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Two forecasts started at the same time must not collide. Before the busy_timeout
// and WAL pragmas one of them died with SQLITE_BUSY while creating the schema.
func TestConcurrentWritersDoNotCollide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	const writers = 6

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := openDB(path) // each goroutine opens it independently
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			d := &Data{
				Days:     []string{day(0), day(1)},
				Names:    []string{"spend"},
				Entities: []string{AccountEntity},
				Values: map[string]map[string][]float64{
					AccountEntity: {"spend": {float64(i), float64(i + 1)}},
				},
			}
			if err := saveData(db, fmt.Sprintf("s%d", i), d); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write failed: %v", err)
	}

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT series_id) FROM series`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers {
		t.Errorf("stored %d series, want %d", n, writers)
	}
}

// The protocol keeps one process alive across requests, so the id matching and
// the per-request counter have to survive being used more than once. The CLI
// currently forecasts once per run, so nothing else exercises this.
func TestWorkerAnswersRepeatedRequests(t *testing.T) {
	withWorker(t, "echo", "echo_worker.py")
	w, err := startWorker("echo")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	series := make([]float64, 40)
	for i := range series {
		series[i] = float64(100 + i)
	}
	for i := 1; i <= 5; i++ {
		q, err := w.Forecast([][]float64{series}, []string{"spend"}, 3,
			w.Shake.Quantiles, nil, nil)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if len(q) != 1 || len(q[0]) != 3 {
			t.Fatalf("request %d: got %d metrics", i, len(q))
		}
	}
	if w.n != 5 {
		t.Errorf("request counter = %d, want 5", w.n)
	}
}
