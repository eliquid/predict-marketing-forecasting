# CLAUDE.md

**Read `AGENTS.md` first. It is the single source of truth** for what this project
is, how to build and test it, the model protocol, the hard rules, and the settled
decisions. Nothing from it is repeated here, so the two cannot drift apart.

This file holds only what is specific to working here as Claude.

---

## Coding style this project follows

Two documents govern all work here. **Read both before writing or changing code:**

- `guidelines/ponytail.md` — the laziest solution that actually works
- `guidelines/karpathy-guidelines.md` — don't assume, stay surgical, define "done"

They are verbatim copies of their upstream sources. Don't edit them; replace them
wholesale if upstream changes.

### The rules broken most often here — hold these especially

1. **Never stall on an answer you can default.** Pick the sane option, state it in
   one line, keep moving. Don't ask the same question twice.
2. **Don't assume — and say what you assumed.** Every assumption goes in writing
   before the work, not after it turns out wrong.
3. **No invented numbers.** No time estimates, sizes or benchmarks unless measured.
   "I don't know" beats a confident guess. This has gone wrong twice: a fabricated
   "months" estimate, and a fabricated "takes minutes" that was really 3 seconds.
4. **Read fully, then be lazy.** The ladder shortens the solution, never the
   reading. Check what already exists here before inventing something.
5. **Three short lines after the code.** Long prose only when explicitly asked for
   (a plan, a report, a walkthrough).
6. **Every changed line traces to the request.** No adjacent improvements.

## Skills

Procedures live in `.claude/skills/`. They are step-by-step tasks, not facts —
facts belong in `AGENTS.md`.

| Skill | Use it when |
|---|---|
| `new-export` | a fresh ad-platform CSV arrives — the recurring job |
| `finetune` | training or retraining the `chronos2ft` adapter |
| `add-a-model` | wiring a third forecasting model in |
| `verify` | proving a change is sound before saying it works |
| `sqlite-optimization` | touching the schema, a pragma, an index or a query plan |

## Recent shape changes

Several things moved, so older notes may mislead:

- The tool forecasts **per campaign and at account level** from a single export
  (`AGENTS.md` §2a). Notes describing one series per file predate this.
- Forecasts are **kept and scored** against the actuals that arrive later
  (`AGENTS.md` §4a, the `forecast_accuracy` view and the `accuracy` command).
- There is a **third model**, `chronos2ft` — Chronos-2 with a LoRA adapter trained
  on the user's own data (`AGENTS.md` §4c). It has not beaten the stock model.
  Any accuracy query must filter `trained_on = 0`.
- The **database is gated on open** (`AGENTS.md` §4a). `foreign_keys` is on,
  `user_version` is checked, the file is 0600, and there is a partial index the
  accuracy queries depend on. A database from an older schema is now refused with
  an explanation rather than failing later on `no such column: entity`.

- Installing prints a **Hugging Face "unauthenticated requests" warning**. It is
  expected, both models are public and ungated, and it is not a failure
  (`AGENTS.md` §2). Do not add token handling to `models/fetch.py` to silence it —
  `huggingface_hub` already reads `HF_TOKEN` from the environment.

- **The export has to be the right download.** Google Ads: download →
  **More options**, segmented **daily**, format **`.csv`** and never
  `.csv (Excel)` —
  the Excel one is UTF-16 tab-separated despite the name and is refused
  (`AGENTS.md` §2a0). This is the most common reason a real file will not load.
- **`data/` and `pm.db` are anchored to the install**, like `models/` always
  was (`defaultPath`). Never reintroduce a bare relative default: it splits the
  forecast history across databases and `accuracy` silently loses it.
- **Reports are written to `data/reports/`**, never beside the export: `data/`
  has to show at a glance what is still waiting to be read.
- **Storing and modelling are separate decisions** (`AGENTS.md` §2a1). Every
  campaign in the export is stored in full — `raw`, `series`, and the `(account)`
  total. Only the campaigns the export's status column says are **switched on as
  of its last day** are forecast or trained on. Both halves of that rule exist:
  one long-dead campaign forecast anyway returned quantiles 6.9% out of order and
  destroyed a whole report. The report dropdowns come from the runs, so they show
  only what was forecast, automatically.
- **`campaignStates` (`ingest.go`) and `CAMPAIGN_STATES` (`finetune.py`) must
  stay in step.** The status column is found by its *values*, never its name —
  a Google Ads export also carries `Status` and `Status reasons`, and matching on
  the word picks the wrong one.
- **The trainer runs every step and says nothing about the data.** No time limit,
  no size check, no `NOTE:` telling the user their dataset is small. It cannot
  act on such a judgement and neither can the reader, and mid-run it reads as a
  failure. Measured facts about how well the adapter does go in `AGENTS.md` §4c;
  they do not go in the program's output.
- **Never put a time limit on training.** A `--budget` wall clock silently cut a
  real run to 1,210 of 2,000 steps and the resulting adapter looked finished in
  every report. Removed everywhere; lower `--steps` instead (`AGENTS.md` §2c).
- **Never put the chart readout inside the scroller.** It scrolls away with the
  content and shows nothing, and a synthetic mousemove in a test will not catch
  it — check it in a browser.
- **The comparison chart is fixed-width inside a scroller, not scaled to fit.**
  That is what makes the crosshair possible; scaling it would break the
  coordinate mapping (`AGENTS.md` §2c).
- **`import` is the recurring job now** (`AGENTS.md` §2c): `data/` in,
  two comparison reports out, CSV filed into `data/imported/`. It refuses under
  90 days. `forecast` is still the single-model command underneath it.
- **`report` redraws the pages from stored runs** (`rerender.go`, `AGENTS.md`
  §2c). It reads only: no CSV, no forecast, no retrain, no write to the
  database. Use it when you have changed how a chart is drawn — re-importing to
  look at a drawing costs the fine-tune. The one thing it cannot restore is the
  `%` sign on a rate, because `series` stores the number and not the sign.
- The **comparison report** (`compare.go`) is deliberately separate from the
  single-model one (`report.go`): different question, different shape. Its
  dropdowns are plain JavaScript on purpose — htmx needs a server a `file://`
  page has not got.

Columns are sorted into forecast / setting / rate / identifier / text by rule
(`AGENTS.md` §4b), and the tool prints which rule it applied to each. When
something is "missing" from a forecast, read that output before suspecting a bug.

## Working style that has paid off here

- **Prove it, don't assert it.** Every claim in this project's docs came from a
  command that was actually run. When you say a thing works, show the output.
- **Attack your own change.** The defects listed at the end of `AGENTS.md` were all
  found by deliberately trying to break the tool, not by reading the code.
- **Fix the test only when the code is right.** Several "failures" here were stale
  assertions, and one was a test helper that generated `2026-01-32`. Check which
  side is wrong before editing either.
