package main

// Round 13: the tool has to work on a CSV that is not in the project folder.
//
// Model paths were resolved against the working directory, so running the binary
// from anywhere else silently found no models at all.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryWorksFromAnotherDirectory(t *testing.T) {
	bin, err := filepath.Abs("predictmarketing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("binary not built; run: go build -o predictmarketing .")
	}
	if _, err := os.Stat(filepath.Join("models", ".venv", "bin", "python")); err != nil {
		t.Skip("no python environment; run predictmarketing setup")
	}

	cmd := exec.Command(bin, "models")
	cmd.Dir = t.TempDir() // deliberately not the project folder
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running from %s failed: %v\n%s", cmd.Dir, err, out)
	}
	s := string(out)
	for _, want := range []string{"chronos2", "timesfm3"} {
		if !strings.Contains(s, want) {
			t.Errorf("model %q missing from output:\n%s", want, s)
		}
	}
	// chronos2ft is legitimately unavailable until someone trains one, so only
	// the two pretrained models have to resolve from another directory.
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, "unavailable") {
			continue
		}
		if strings.HasPrefix(line, "chronos2ft") {
			continue
		}
		t.Errorf("a pretrained model could not be located from another directory: %s", line)
	}
}

// Two ~70-line scripts do not need a bytecode cache cluttering models/.
func TestWorkersDoNotWritePycache(t *testing.T) {
	if _, err := os.Stat(filepath.Join("models", ".venv")); err != nil {
		t.Skip("no python environment")
	}
	cache := filepath.Join("models", "__pycache__")
	os.RemoveAll(cache)
	w, err := startWorker("chronos2")
	if err != nil {
		t.Skip("chronos2 unavailable: " + err.Error())
	}
	w.Close()
	if _, err := os.Stat(cache); err == nil {
		t.Errorf("%s was created; PYTHONDONTWRITEBYTECODE should prevent it", cache)
	}
}
