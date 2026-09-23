package main

// The installer is the first thing a new person runs, and it is the easiest
// thing to break by renaming a file. These checks are cheap and catch that.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func installScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallScriptIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if out, err := exec.Command(sh, "-n", "install.sh").CombinedOutput(); err != nil {
		t.Fatalf("install.sh is not valid shell: %v\n%s", err, out)
	}
	if out, err := exec.Command(sh, "-n", "build-dist.sh").CombinedOutput(); err != nil {
		t.Fatalf("build-dist.sh is not valid shell: %v\n%s", err, out)
	}
}

// Every path the installer names must exist, or a rename silently breaks setup
// for everyone who has not installed yet.
func TestInstallScriptReferencesRealFiles(t *testing.T) {
	for _, p := range []string{
		"models/requirements.txt",
		"models/.venv/bin/python",
		"testdata/example.csv",
		"README.md",
	} {
		if !strings.Contains(installScript(t), p) {
			continue // not referenced, nothing to check
		}
		if isGenerated(p) {
			continue // the installer creates it; it is absent until it runs
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("install.sh refers to %s, which does not exist", p)
		}
	}
}

// The installer must run the program the same way the docs tell people to.
func TestInstallScriptUsesRealCommands(t *testing.T) {
	sh := installScript(t)
	for _, cmd := range regexp.MustCompile(`\./predictmarketing (\w+)`).FindAllStringSubmatch(sh, -1) {
		name := cmd[1]
		b, err := os.ReadFile("main.go")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `case "`+name+`"`) {
			t.Errorf("install.sh runs %q, which is not a command in main.go", name)
		}
	}
}

func TestInstallScriptIsExecutable(t *testing.T) {
	for _, p := range []string{"install.sh", "build-dist.sh"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&0o111 == 0 {
			t.Errorf("%s is not executable; a new user would have to know to run `sh %s`", p, p)
		}
	}
}

// share.sh must not ship anything generated from the author's own account. The
// report exclusion silently stopped matching when the model name was added to the
// default filename ("x_forecast.html" became "x_forecast_chronos2.html"), so real
// campaign names travelled in every bundle until it was caught.
func TestShareScriptExcludesGeneratedFiles(t *testing.T) {
	b, err := os.ReadFile("share.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, want := range []string{
		"*_forecast*.html", // reports, whichever model made them
		"*.db",             // the database itself
		"models/finetuned", // an adapter fitted to one person's data
		"models/.venv",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("share.sh does not exclude %q", want)
		}
	}
	// The pattern must actually match what the tool writes.
	name := "examples/02-marketing_forecast_chronos2.html"
	if ok, _ := filepath.Match("*_forecast*.html", filepath.Base(name)); !ok {
		t.Errorf("the exclusion pattern does not match a real report name: %s", name)
	}
}

// The weights are not in the repository, and the README has to be straight about
// why: TimesFM's licence forbids redistribution, and both files are far over
// GitHub's per-file limit. Chronos-2 is Apache-2.0 and is mirrored, so the
// override that makes the mirror usable must stay wired up.
func TestWeightsDistributionIsDocumented(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	for _, want := range []string{
		"download themselves",    // nobody has to find them
		"Distribute the TimesFM", // why TimesFM is not mirrored
		"PM_CHRONOS2_DIR",        // the documented escape hatch
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README no longer explains %q", want)
		}
	}

	f, err := os.ReadFile(filepath.Join("models", "fetch.py"))
	if err != nil {
		t.Fatal(err)
	}
	fetch := string(f)
	if !strings.Contains(fetch, "PM_") || !strings.Contains(fetch, "EXPECTED") {
		t.Error("fetch.py no longer supports a verified local weights directory")
	}
	// The hashes the README and the release notes quote must be the ones enforced.
	for _, sha := range []string{
		"ddcda3c7508bf2528087723e98a20707cc04b7f370ae275a9fd88078ddba4f42", // chronos2
		"a7592b0a8432baee54483254e5647856911ce69e09d09a9bb65904b2d98f17da", // timesfm3
	} {
		if !strings.Contains(fetch, sha) {
			t.Errorf("fetch.py does not pin %s", sha)
		}
	}
}

// Nothing may refuse to install over a version number. The go directive is the
// floor the dependencies actually impose, not the newest toolchain that happened
// to be on the author's laptop, and the installer prefers the tested Python
// without insisting on it.
func TestNoHardcodedVersionLocks(t *testing.T) {
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindStringSubmatch(string(mod))
	if m == nil {
		t.Fatal("no go directive in go.mod")
	}
	minor, _ := strconv.Atoi(m[2])
	// 1.26 was the directive for a while and shut out everyone on 1.24 and 1.25
	// for no reason: no dependency asked for it.
	if minor > 24 {
		t.Errorf("go.mod requires go 1.%d; the dependencies only need 1.24, so this "+
			"excludes users needlessly", minor)
	}

	script := installScript(t)
	// A bare --python 3.11 with no fallback is a lock.
	if strings.Contains(script, "--python 3.11") && !strings.Contains(script, ">=3.10") {
		t.Error("install.sh pins Python 3.11 with no fallback for other versions")
	}
	// Reporting a version is fine and useful ("built from source with go1.26.5").
	// Refusing to continue because of one is not, so look for comparisons rather
	// than mentions.
	for _, gate := range []string{
		`die "Go 1`, `die "Python 3`, `die "SQLite`,
		"sqlite3 --version", "requires Go 1", "requires Python 3",
	} {
		if strings.Contains(script, gate) {
			t.Errorf("install.sh appears to refuse over a version: %q", gate)
		}
	}
}
