package main

// Round 8: the recorded weights sha256 must be a verified fact, not a claim
// copied out of a file at download time.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runWeightsCheck calls the Python verifier directly against a throwaway
// weights.json, so the real 1.7 GB of weights are never touched.
func runWeightsCheck(t *testing.T, recordedSHA string, body []byte) (string, error) {
	t.Helper()
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "model.safetensors"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"fake": map[string]any{
		"repo": "r", "revision": "v", "path": snap,
		"weights_sha256": recordedSHA, "bytes": len(body)}}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "weights.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	check, _ := filepath.Abs(filepath.Join("models", "weights_check.py"))
	py, _ := filepath.Abs(filepath.Join("models", ".venv", "bin", "python"))
	if _, err := os.Stat(py); err != nil {
		t.Skip("no python environment; run predictmarketing setup")
	}
	script := "import sys; sys.path.insert(0, " + quoteGo(filepath.Dir(check)) + "); " +
		"from weights_check import load_verified; " +
		"print(load_verified(" + quoteGo(dir) + ", 'fake')['revision'])"
	cmd := exec.Command(py, "-c", script)
	// Same rule as the real workers: do not leave a bytecode cache in models/.
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func quoteGo(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestWeightsVerificationAcceptsTheRealFile(t *testing.T) {
	body := []byte("pretend these are model weights")
	sum := sha256.Sum256(body)
	out, err := runWeightsCheck(t, hex.EncodeToString(sum[:]), body)
	if err != nil {
		t.Fatalf("matching weights must be accepted, got %v\n%s", err, out)
	}
}

// One flipped bit in half a gigabyte was caught in practice; this is the same
// check in miniature.
func TestWeightsVerificationCatchesAChangedFile(t *testing.T) {
	body := []byte("pretend these are model weights")
	sum := sha256.Sum256(body)
	tampered := append([]byte{}, body...)
	tampered[len(tampered)/2] ^= 0x01

	out, err := runWeightsCheck(t, hex.EncodeToString(sum[:]), tampered)
	if err == nil {
		t.Fatal("changed weights must be refused")
	}
	if !strings.Contains(out, "not the ones that were downloaded") {
		t.Errorf("error should explain the mismatch, got:\n%s", out)
	}
	if !strings.Contains(out, "Refusing to forecast") {
		t.Errorf("error should say it is refusing, got:\n%s", out)
	}
}
