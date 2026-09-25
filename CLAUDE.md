# CLAUDE.md

**Read `AGENTS.md` first. It is the single source of truth** for what this project
is, how to build and test it, the model protocol, the hard rules, and the settled
decisions. Nothing here re-argues it: where this file summarises a rule it names
the section to check, and `AGENTS.md` wins when the two disagree.

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
| `add-a-model` | wiring another forecasting model in alongside the three there are |
| `verify` | proving a change is sound before saying it works |
| `sqlite-optimization` | touching the schema, a pragma, an index or a query plan |
| `finish-an-import` | an import was interrupted, crashed, or wrote only one report |
| `refused-forecast` | a model returned an unusable forecast, hung, or its quantiles look wrong |
| `db-repair` | the database is refused on open, or `accuracy` says nothing on a database that has runs |
| `hand-off` | packaging this folder for someone else — check what you are actually sending |

## Recent shape changes

Several things moved, so older notes may mislead:

- The tool forecasts **per campaign and at account level** from a single export
  (`AGENTS.md` §2a). Notes describing one series per file predate this.
- Forecasts are stored so they **can** be scored against the actuals that arrive
  later (`AGENTS.md` §4a, the `forecast_accuracy` view and the `accuracy`
  command) — but `import` empties the database, so that only works across
  repeated `forecast` runs, never across imports.
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
  **More options**, segmented **daily**, date range **ending yesterday**, format
  **`.csv`** and never `.csv (Excel)` — the Excel one is UTF-16 tab-separated
  despite the name and is refused (`AGENTS.md` §2a0). This is the most common
  reason a real file will not load.
- **The range must end on the last full day, never today.** A day still running
  is a partial day, and nothing here can tell it from a real slump: measured, the
  same 2026-09-23 read 4,435.52 taken mid-afternoon and 6,378.35 once complete,
  and forecasting from the partial one put the next 7 days **28% low on both
  models**. The tool does not check this and cannot — the file does not say when
  it was produced (`AGENTS.md` §2a0).
- **`data/` and `pm.db` are anchored to the install**, like `models/` always
  was (`defaultPath`). Never reintroduce a bare relative default: it splits the
  forecast history across databases and `accuracy` silently loses it.
- **`import` and `report` write to `data/reports/`**, never beside the export:
  `data/` has to show at a glance what is still waiting to be read. (`forecast`
  is the exception and always was — its single-model page lands next to the CSV
  unless `-out` says otherwise.)
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
- **`import` empties the whole database first** (`AGENTS.md` §4a) — forecasts,
  runs, series and raw. An import is a new account. The wipe happens *after every model has
  answered* — `forecastModel` writes nothing, `storeRun` writes what was already
  computed — so a model that refuses leaves the database untouched rather than
  emptied. Only the first file of a batch wipes. **This means `accuracy` cannot score anything
  across imports**: the forecast is deleted by the import that brings its
  actuals. `forecast` does not wipe and is the route to a scoreable history.
- **Storage replaces, it does not merge.** `saveData` and `saveRaw` both delete
  the whole `series_id`/source before inserting, so `series` and `raw` always
  describe the same file and re-running `forecast` on a newer export — including
  one for a different account under the same name — discards the previous
  dataset. Runs and forecasts survive *that*; they do not survive an `import`,
  which wipes them too (the bullet above).
- **The view and the indexes live in `derivedObjects`, not `schema`.** They are
  compared and rebuilt on every open, so changing one needs no `schemaVersion`
  bump — and putting a new one back in `schema` behind `IF NOT EXISTS` reinstates
  a bug where a stale view answered queries wrongly instead of failing.
- **`import` tells the trainer which columns the file has.** `finetune.py`'s
  defaults (`Cost,Impr.,Clicks`, `Campaign`) are Google-Ads-shaped and only
  `examples/05-campaigns.csv` carries them; `trainFinetune` passes `data.Names`
  and `data.GroupBy` instead. A real Meta export is what found this — the trainer
  stopped with `columns not in <file>: ['Impr.']` and report 2 never got written.
  The defaults still apply when you run the trainer **by hand**, where the silent
  failure is the bad one: no matching group column means `training on 1 series`,
  everything collapsed into `(account)` (`AGENTS.md` §4c).
- **`load_series` must skip exactly what the forecaster skips** — switched off,
  never moved, *and* stopped (rows ending before the file's last day). Three
  rules, both sides. On the Meta export the forecaster ran 11 campaigns; before
  the third rule the trainer would have fitted 13, two of them dead tails.
- **`num()` in `models/finetune.py` must stay in step with `parseCell`.** They
  diverged in both directions once: NaN trained on, ordinary cells crashing.
- **Never put a time limit on training.** A `--budget` wall clock silently cut a
  real run to 1,210 of 2,000 steps and the resulting adapter looked finished in
  every report. Removed everywhere; lower `--steps` instead (`AGENTS.md` §2c).
- **Never put the chart readout inside the scroller.** It scrolls away with the
  content and shows nothing, and a synthetic mousemove in a test will not catch
  it — check it in a browser.
- **The comparison chart shades q10-q90 behind each drawn line**, inside the
  series group so the legend hides both together. It deliberately had no bands
  while it drew six lines — six translucent bands are unreadable — and that
  rationale expired when the report went down to one or two (`AGENTS.md` §2c).
  Nine quantiles were always stored; only the median was ever drawn.
- **The comparison chart is fixed-width inside a scroller, not scaled to fit** —
  for **legibility**, not for the crosshair. `fromEvent` scales the pointer
  offset by `viewBox.width / rect.width` before looking up the day, so the
  crosshair survives any uniform scaling (measured at 5.25x). Keep the fixed width; do not defend it with the coordinate-mapping
  reason, which is false (`AGENTS.md` §2c).
- **`import` is the recurring job now** (`AGENTS.md` §2c): `data/` in,
  two comparison reports out, CSV filed into `data/imported/`. It refuses under
  90 days. `forecast` is still the single-model command underneath it.
- **Every import forecasts three windows** — whole file, last 270 days, last 90 —
  with both pretrained models, and **stores all six**. Only `average@90d`, the
  mean of the two 90-day runs, is **drawn** (`AGENTS.md` §2c). Report 1 is
  actuals + the average; report 2 adds `chronos2ft@full`. A window longer than
  the file, or exactly as long as it, is **skipped and announced**, never an
  error; below 90 days nothing runs and the message names the 90-day gate, not
  the 32-day model floor.
- **The average is the 90-day window, not all of them.** A walk-forward backtest
  over 31 origins had the 90-day window beating the whole file and 270 days at
  **every one of the 7 horizons and the pooled total, at both levels — 16 of
  16**, by ~1.7 points — about 2.5x the gap
  between the two models. On a file of exactly 90 days the 90d window is skipped
  as a duplicate, so the average falls back to `full`, which is the last 90 days
  there.
- **The window is in the stored model name** (`chronos2@90d`), because `accuracy`
  groups by that column and the point is to learn which history length forecasts
  best. `runLabel` builds it; the worker is started by the bare name.
- **`lastDays` trims numbers only.** Names, entities, group column and exclusion
  lists stay as the whole file decided them, or a short window could classify a
  campaign differently and `writeComparison`'s intersection would silently drop
  it from every chart.
- **`average@90d` excludes `chronos2ft`** and any run with a different quantile
  grid. Trained on the data it would be averaged into, and it has not beaten
  stock (`AGENTS.md` §4c). The fine-tune gets the **whole file**, never the
  90-day window — it learns from the data rather than reading it.
- **`report` redraws the pages from stored runs** (`rerender.go`, `AGENTS.md`
  §2c). It reads only: no CSV, no forecast, no retrain, no write to the
  database. Use it when you have changed how a chart is drawn — re-importing to
  look at a drawing costs the fine-tune. The one thing it cannot restore is the
  `%` sign on a rate, because `series` stores the number and not the sign.
- The **comparison report** (`compare.go`) is deliberately separate from the
  single-model one (`report.go`): different question, different shape. Its
  dropdowns are plain JavaScript on purpose — htmx needs a server a `file://`
  page has not got.

- **`-fill-absent` handles ragged exports** (`AGENTS.md` §2a2): platforms that
  list a campaign only on the days it ran. **Nothing in the code knows which ad
  network a file came from, and nothing should** — it is the shape of the data
  that decides. Three parts hold it together: fill only *outside* each campaign's
  run (a hole in the middle is a broken download and stays refused); keep the
  synthesised rows out of `raw`, which records what the platform actually sent;
  and treat a campaign the export stops listing as **stopped** (`d.Stopped`,
  a third subset of `d.Inactive`), stored but not forecast. Without that last
  part the first real run died with quantiles 67.3% out of order — the same
  dead-campaign failure the paused rule exists to prevent. The flag is a no-op
  on a dense export, measured line for line on a 1,099-day one.

Columns are sorted into forecast / setting / rate / identifier / text by rule
(`AGENTS.md` §4b). `forecast` prints which rule it applied to each; **`import`
prints only the `forecasting:` line** and nothing about the columns it set
aside. When something is "missing" from a forecast, read that output before
suspecting a bug — and on the recurring job, notice that a demoted column simply
stops appearing in that list.

That classification is also what decides the **campaign column**: only a label
qualifies — a text column, or a numeric one `looksLikeIdentifier` accepts
(`Campaign ID`). A measured column is never chosen however well its value count
fits, because splitting on `Cost` turns prices into campaign names and drops the
metric from the forecast, which it once did silently (`AGENTS.md` §2a).

## Working style that has paid off here

- **Prove it, don't assert it.** Every claim in this project's docs came from a
  command that was actually run. When you say a thing works, show the output.
- **Attack your own change.** The defects listed at the end of `AGENTS.md` were all
  found by deliberately trying to break the tool, not by reading the code.
- **Fix the test only when the code is right.** Several "failures" here were stale
  assertions, and one was a test helper that generated `2026-01-32`. Check which
  side is wrong before editing either.
