package main

// Round 2: what happens when a model misbehaves.
//
// A model is a separate process that can crash, stall, or answer nonsense. Go
// must report which model failed and why, never hang silently and never store a
// forecast it could not verify. Each case below is driven by a deliberately
// broken worker in testdata/badworkers.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withWorker registers a fake model for the duration of one test.
//
// The fake workers are Python scripts, so they need the environment install.sh
// builds. Without it these tests used to fail rather than skip, which meant a
// freshly unpacked share bundle reported ten failures before anyone had a chance
// to run the installer.
func withWorker(t *testing.T, name, script string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join("models", ".venv", "bin", "python")); err != nil {
		t.Skip("no python environment; run ./install.sh")
	}
	abs, err := filepath.Abs(filepath.Join("testdata", "badworkers", script))
	if err != nil {
		t.Fatal(err)
	}
	models[name] = abs
	t.Cleanup(func() { delete(models, name) })
}

func forecastFrom(t *testing.T, name string) ([][][]float64, error) {
	t.Helper()
	w, err := startWorker(name)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	series := make([]float64, 40)
	for i := range series {
		series[i] = float64(100 + i)
	}
	return w.Forecast([][]float64{series}, []string{"spend"}, 3, w.Shake.Quantiles, nil, nil)
}

func TestWorkerThatDiesMidRequest(t *testing.T) {
	withWorker(t, "crash", "crash_worker.py")
	_, err := forecastFrom(t, "crash")
	if err == nil {
		t.Fatal("a model that exits mid-request must be an error")
	}
	if !strings.Contains(err.Error(), "crash") {
		t.Errorf("error should name the model, got: %v", err)
	}
}

func TestWorkerThatSendsGarbage(t *testing.T) {
	withWorker(t, "garbage", "garbage_worker.py")
	_, err := forecastFrom(t, "garbage")
	if err == nil {
		t.Fatal("unparseable output must be an error")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A reply carrying the wrong id means the stream has desynchronised; accepting it
// would attach one series' forecast to another.
func TestWorkerThatAnswersTheWrongRequest(t *testing.T) {
	withWorker(t, "wrongid", "wrongid_worker.py")
	_, err := forecastFrom(t, "wrongid")
	if err == nil {
		t.Fatal("a mismatched reply id must be an error")
	}
	if !strings.Contains(err.Error(), "answered request") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWorkerThatNeverSaysHello(t *testing.T) {
	withWorker(t, "nohandshake", "nohandshake_worker.py")
	_, err := forecastFrom(t, "nohandshake")
	if err == nil {
		t.Fatal("a model that exits before the handshake must be an error")
	}
	if !strings.Contains(err.Error(), "before saying hello") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWorkerThatReturnsTheWrongShape(t *testing.T) {
	withWorker(t, "badshape", "badshape_worker.py")
	_, err := forecastFrom(t, "badshape")
	if err == nil {
		t.Fatal("a forecast with the wrong number of days must be rejected")
	}
	if !strings.Contains(err.Error(), "unusable forecast") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Close must not block on a model that ignores stdin closing.
func TestCloseDoesNotBlockOnAStalledWorker(t *testing.T) {
	withWorker(t, "hang", "hang_worker.py")
	w, err := startWorker("hang")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Close blocked on a stalled model")
	}
}

// A stuck model must not hang the command forever. The timeout is 5 minutes in
// normal use; the test sets it short so it can actually run.
func TestStuckModelTimesOutInsteadOfHanging(t *testing.T) {
	withWorker(t, "hang2", "hang_worker.py")
	w, err := startWorker("hang2")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Timeout = 2 * time.Second

	start := time.Now()
	_, err = w.Forecast([][]float64{make([]float64, 40)}, []string{"spend"}, 3,
		w.Shake.Quantiles, nil, nil)
	if err == nil {
		t.Fatal("a stuck model must produce an error, not a forecast")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("gave up after %v, expected about 2s", took)
	}
	if !strings.Contains(err.Error(), "stuck rather than busy") {
		t.Errorf("error should explain what happened, got: %v", err)
	}
}

// A worker that refuses to start explains why over several lines. The whole
// explanation must survive, not just the "run this" tail.
func TestWorkerFailureKeepsItsExplanation(t *testing.T) {
	got := lastLine("Loading weights: 100%\n" +
		"chronos2ft: no fine-tuned model yet. Train one with:\n" +
		"  models/.venv/bin/python models/finetune.py YOUR.csv\n")
	if !strings.Contains(got, "no fine-tuned model yet") {
		t.Errorf("the reason was lost: %q", got)
	}
	if !strings.Contains(got, "finetune.py") {
		t.Errorf("the instruction was lost: %q", got)
	}
	if got := lastLine("only one line\n"); got != "only one line" {
		t.Errorf("single line case: %q", got)
	}
	if got := lastLine("   \n\n"); got != "" {
		t.Errorf("empty case: %q", got)
	}
}
