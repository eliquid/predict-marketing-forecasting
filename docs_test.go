package main

// Documentation rots silently. These checks are cheap and catch a rename that
// would otherwise leave an agent following instructions to files that moved.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// isGenerated reports whether a path is something this project creates rather
// than ships: the Python environment and weights install.sh downloads, and the
// LoRA adapter and registry that training writes. share.sh deliberately excludes
// all of them, so asserting that they exist passes in a working copy and fails in
// the bundle a recipient actually unpacks -- which is where these checks matter
// most.
func isGenerated(p string) bool {
	for _, gen := range []string{
		"models/.venv", "models/cache", "models/weights.json",
		"models/finetuned", "models/finetuned.json",
	} {
		if p == gen || strings.HasPrefix(p, gen+"/") {
			return true
		}
	}
	return false
}

// Paths named in AGENTS.md must exist. Only paths that look like real files are
// checked -- prose mentioning a directory in passing is not.
func TestAgentsDocReferencesRealFiles(t *testing.T) {
	b, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("`([a-zA-Z0-9_./-]+\\.(go|py|sh|md|csv|txt|json))`")
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		p := m[1]
		// Skip naming patterns rather than paths ("_worker.py", "*_test.go"), and
		// anything under the generated Python environment.
		if seen[p] || strings.HasPrefix(p, "_") || strings.HasPrefix(p, "*") ||
			isGenerated(p) {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			t.Errorf("AGENTS.md refers to %s, which does not exist", p)
		}
	}
	if len(seen) < 5 {
		t.Errorf("only %d file references found; did the doc lose its content?", len(seen))
	}
}

// AGENTS.md is the single source of truth; the others must point at it rather
// than restating rules that would then drift.
func TestOtherDocsPointAtAgentsDoc(t *testing.T) {
	for _, p := range []string{
		"CLAUDE.md",
		".claude/skills/add-a-model/SKILL.md",
		".claude/skills/verify/SKILL.md",
		".claude/skills/new-export/SKILL.md",
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s missing: %v", p, err)
			continue
		}
		if !strings.Contains(string(b), "AGENTS.md") {
			t.Errorf("%s should point at AGENTS.md instead of restating it", p)
		}
	}
}

// Every skill needs the frontmatter an agent uses to decide whether to load it.
func TestSkillsHaveFrontmatter(t *testing.T) {
	for _, p := range skillFiles(t) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.HasPrefix(s, "---\n") {
			t.Errorf("%s has no frontmatter block", p)
			continue
		}
		for _, field := range []string{"name:", "description:"} {
			if !strings.Contains(s[:strings.Index(s[4:], "---")+4], field) {
				t.Errorf("%s frontmatter is missing %s", p, field)
			}
		}
	}
}

// skillFiles finds every skill, so a new one cannot be added without its
// frontmatter being checked.
func skillFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".claude/skills")
	if err != nil {
		t.Skip("no skills directory")
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(".claude/skills", e.Name(), "SKILL.md"))
		}
	}
	if len(out) == 0 {
		t.Fatal("no skills found")
	}
	return out
}

// Every skill must point at AGENTS.md rather than restating it.
func TestSkillsPointAtAgentsDoc(t *testing.T) {
	for _, p := range skillFiles(t) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "AGENTS.md") {
			t.Errorf("%s should point at AGENTS.md instead of restating it", p)
		}
	}
}

func TestExampleFilesLoad(t *testing.T) {
	for _, p := range []string{
		"examples/01-simple.csv",
		"examples/02-marketing.csv",
		"examples/03-platform-export.csv",
		"examples/04-with-budget.csv",
	} {
		d, err := readCSV(p, nil, "")
		if err != nil {
			t.Errorf("%s does not load: %v", p, err)
			continue
		}
		if len(d.Names) == 0 {
			t.Errorf("%s has no numeric columns", p)
		}
		if len(d.Days) < smallestUsefulSeries {
			t.Errorf("%s has only %d rows", p, len(d.Days))
		}
	}
}

// Every file a README command names must ship, because the README is the first
// thing someone who just cloned this will try to run. Two stale samples got here
// by being written by hand: one showed a pre-multivariate output format the tool
// no longer produces, and both named an export that exists only on the author's
// machine.
func TestReadmeCommandsNameFilesThatShip(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	// Placeholders that are meant to stand in for the reader's own export.
	placeholder := map[string]bool{"data.csv": true, "your-export.csv": true}

	re := regexp.MustCompile(`\./predictmarketing forecast ("[^"]+"|[^ \n]+)`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		f := strings.Trim(m[1], `"`)
		if placeholder[f] {
			continue
		}
		found++
		if _, err := os.Stat(f); err != nil {
			t.Errorf("README runs `forecast %s`, which is not in the repository", f)
		}
	}
	if found == 0 {
		t.Error("no runnable forecast command found in README; did the examples go?")
	}
}

// The examples must cover the headline feature. Campaign-level forecasting is the
// reason this tool exists, and for a while no shipped file demonstrated it: the
// one with a Campaign column had a single campaign per day, so -by Campaign
// refused it.
func TestAnExampleHasSeveralCampaignsPerDay(t *testing.T) {
	paths, err := filepath.Glob("examples/*.csv")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		d, err := readCSV(p, nil, "")
		if err != nil {
			continue
		}
		if d.RowsPerDay > 1 && len(d.Entities) > 2 {
			return // account plus at least two campaigns
		}
	}
	t.Error("no example file has more than one campaign per day, so nothing " +
		"demonstrates per-campaign forecasting")
}

// Every flag the program accepts must appear in its own help, and nothing in the
// help may name a flag that does not exist. -columns, -entities, -by and -history
// were all live for weeks while `--help` listed none of them, which made the
// per-campaign feature undiscoverable from the command line.
func TestHelpListsEveryFlag(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`fs\.(?:String|Int|Bool)\("([a-z-]+)"`).
		FindAllStringSubmatch(string(src), -1) {
		defined[m[1]] = true
	}
	if len(defined) < 5 {
		t.Fatalf("only found %d flags in main.go; the pattern stopped matching", len(defined))
	}

	var help strings.Builder
	usageTo(&help)
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^  -([a-z-]+)`).
		FindAllStringSubmatch(help.String(), -1) {
		listed[m[1]] = true
	}
	for f := range defined {
		if !listed[f] {
			t.Errorf("-%s is accepted but missing from the help", f)
		}
	}
	for f := range listed {
		if !defined[f] {
			t.Errorf("the help lists -%s, which the program does not accept", f)
		}
	}
}

// A public tool needs to be able to say what it is: every bug report starts with
// a version. The values come from the build information Go embeds, so this checks
// the plumbing rather than a hardcoded constant.
func TestVersionReports(t *testing.T) {
	var b strings.Builder
	printVersion(&b)
	out := b.String()
	for _, want := range []string{"predictmarketing", "module", "commit", "go "} {
		if !strings.Contains(out, want) {
			t.Errorf("version output has no %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "github.com/eliquid/predict-marketing-forecasting") {
		t.Errorf("version does not name the module path:\n%s", out)
	}
	// The help must offer it, or nobody will find it.
	var help strings.Builder
	usageTo(&help)
	if !strings.Contains(help.String(), "version") {
		t.Error("the help does not mention the version command")
	}
}
