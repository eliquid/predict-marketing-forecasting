# AGENTS.md — Predict Marketing

Instructions for any AI agent working on this project (Claude, Codex, Gemini,
Qwen, Hermes, Cursor, or a human who likes checklists).

**This file is the single source of truth.** `CLAUDE.md` and `.claude/skills/`
point here rather than restating anything, so they cannot drift out of step.

---

## 1. What this is

A Go command-line tool that forecasts daily marketing numbers — spend,
impressions, clicks, and anything else numeric — for a whole account **and** for
each campaign in it, using one of two pretrained time-series models:

| Model | Source | Params | Licence |
|---|---|---|---|
| `timesfm3` | Google, `google/timesfm-3.0-pytorch` | 330,710,976 | **non-commercial** |
| `chronos2` | Amazon, `amazon/chronos-2` | 119,477,664 | Apache-2.0 |
| `chronos2ft` | Chronos-2 + a LoRA adapter trained on your own data | 119,477,664 + 4.9 MB adapter | Apache-2.0 |

The models run **unmodified** in Python behind a thin adapter. Go does everything
else: reading CSVs, validating, storing, charting, provenance.

It is **standalone**. Nothing here is vendored from either model's own
repository, and a local checkout of one is not a source: do not read from it,
compare against it, or port anything out of it. The models are used exactly as
published, through the adapter in `models/`.

## 2. Getting oriented

```bash
./install.sh                # one-time setup: Python env, build, model weights (~2.5 GB)
                            # sets up chronos2 and timesfm3; chronos2ft needs YOUR data
go build -o predictmarketing .
go test ./...               # ~143 tests, about 30s
go vet ./... && gofmt -l .  # must be silent
./predictmarketing forecast testdata/example.csv -model chronos2
```

**The Hugging Face warning during install is expected.** `models/fetch.py` downloads
anonymously and prints:

```
Warning: You are sending unauthenticated requests to the HF Hub. Please set a
HF_TOKEN to enable higher rate limits and faster downloads.
```

Both models are **public and ungated** — verified against the Hub API, and a
clean install on an untouched machine pulled all 1.8 GB with no token set. Do not
treat this warning as a failure, and do not add token handling to `models/fetch.py` to
silence it: `huggingface_hub` already reads `HF_TOKEN` and
`HUGGING_FACE_HUB_TOKEN` from the environment on its own, so
`export HF_TOKEN=... && ./install.sh` works with no code change. A token helps
only against per-IP rate limits (shared connections, CI, repeated installs); it
does nothing if Hugging Face is blocked outright, which is what the Chronos-2
mirror in Releases is for.

Commands: `setup`, `models`, `import`, `forecast`, `runs`, `accuracy`.

## 2c. The import workflow

`import` is the recurring job and the path most users take. `forecast` is the
single-model command underneath it, still there for one-off questions.

**Defaults are anchored to the installation, not the shell** (`defaultPath`).
`models/` always resolved relative to the binary; `data/` and `pm.db` did not,
so running from another directory made a second empty `data/` and a second
database while still loading the models — which is what made it look like it had
worked. Forecasts split across databases cannot be scored, so `accuracy` would
quietly have less history than the user believes. `-data` and `-db` still win
when given.

| Step | Where it lives |
|---|---|
| Read every new CSV in `data/` | `pendingFiles`, creates the folder and a note if absent |
| Refuse under **90 days**; 365 better, 730 best | `enoughHistory` / `historyVerdict` |
| Forecast each model over every entity | `runModel` |
| Report 1 into `data/reports/`: `chronos2` + `timesfm3` | `reportPath`, then `writeComparison` |
| Move the CSV to `data/imported/` | `fileAway`, never overwrites |
| Reports go to `data/reports/`, never beside the export | `reportPath` — `data/` is meant to show at a glance what is still unread |
| Train `chronos2ft`, then report 2 with all three | `trainFinetune`, then `writeComparison` again |

**`chronos2ft` is retrained on every import**, on the newest data, which costs
a real stretch of CPU each time -- about 17 minutes on a two-year, fourteen-campaign
export, and longer with more data. That is deliberate: an adapter fitted to
last quarter's numbers quietly goes stale, and a stale model that still looks
current is worse than no model. `-no-finetune` writes report 1 and stops, for
when you only want the two pretrained models.

**Training is never time-limited.** `models/finetune.py` once took a `--budget` wall
clock and `import` passed 600 seconds. On a real export that stopped training at
step 1,210 of 2,000 -- 0.605 of an epoch -- and produced an adapter that was
undertrained while appearing in every report as a peer of the pretrained models.
Nothing in the reports said so. The flag, the callback and the registry field are
gone. Do not reintroduce a time limit in any form: if training is too slow, lower
`--steps`, which is honest about what was asked for.

**Why two reports.** The third model has to be trained on the user's own data
first, which takes as long as it takes. Report 1 is written and the CSV filed away
*before* training starts, so a fine-tune that fails leaves a completed import
and a readable report rather than nothing.

**The comparison report** (`compare.go`, `compare_template.go`) is separate from
the single-model report in `report.go`, because it answers a different question:
not "what does this model say" but "do the models agree". Every model's median is
drawn on one set of axes over the same history. There are no uncertainty bands —
three overlapping translucent bands are unreadable, and agreement is what the
page is for.

Every (entity, metric) pane is rendered into the page and all but one hidden; two
dropdowns swap them with a few lines of plain JavaScript. Not htmx, which needs a
server a `file://` page does not have, and not a chart library, which would be a
download it cannot make. Without JavaScript nothing is hidden and the page
degrades to every chart stacked, which is longer but complete.

Entities and metrics are **intersected** across runs, so a dropdown never offers
a combination some model cannot draw.

**Forecast days are drawn ~5x wider than history days** (`fcPx` vs `histPx` in
`drawCompareChart`), and the svg is emitted at a **fixed pixel width** inside a
scroller rather than scaled to fit. Both are load-bearing: equal spacing left the
forecast a few pixels wide with all the lines and labels on top of each other,
and a fixed width means a pointer position inside the svg *is* a chart
coordinate, which is how the crosshair finds the day without re-deriving the
projection in JavaScript. `drawCompareChart` returns the drawing **and** the
series as JSON for exactly that reason.

Each series is drawn inside `<g class="series" data-model="...">`, and the end
labels inside `<g class="series-label" data-model="...">`, so the legend hides a
line, its markers and its label with one selector and no knowledge of the
drawing. The **readout sits above the chart, outside the scroller**: the first
version put it inside, where it scrolled away with the content and showed
nothing, while a synthetic mousemove in a test found it perfectly.

`-history 0` (the import default) draws every day there is. Scrolling back is
only useful if there is something behind you.

The page is dark and monospaced deliberately: it is dense and numeric, a tabular
font keeps columns of money aligned, and a fixed palette means a screenshot looks
the same to everyone. Line identity is carried by **dash pattern as well as
colour** (`dashFor`/`strokeFor`), so the chart survives greyscale and colour
blindness. End labels are pushed apart by `spreadLabels`, because models that
agree finish at the same height and would otherwise print their names on top of
each other exactly when the chart is most worth reading.

`forecast` flags:

| | |
|---|---|
| `-model` | `chronos2` (default) or `timesfm3` |
| `-horizon N` | days ahead, default 7 |
| `-history N` | days of past data drawn on the chart, default 90 |
| `-columns A,B` | which columns to forecast (commas) |
| `-entities A;B` | which campaigns (**semicolons** — campaign names contain commas) |
| `-by NAME` | column separating campaigns, if it cannot be worked out |
| `-future K=v,v` | known-future values for a column |
| `-series NAME` | dataset name, default the file name |
| `-db` / `-out` | database and report paths |

`import` flags:

| | |
|---|---|
| `-data DIR` | folder to read from, default `data` |
| `-horizon N` | days ahead, default 7 |
| `-history N` | days of past data drawn on the charts, default 90 |
| `-no-finetune` | write report 1 only, skipping the ten-minute retrain |
| `-db FILE` | database file, default `pm.db` |

Longer checks:

```bash
go test -race -count=2 ./...                          # state leaking between tests
go test -run '^$' -fuzz FuzzReadCSV -fuzztime 60s     # coverage-guided fuzzing
```

If `go test` skips model tests, the Python environment is missing — run `./install.sh`.

## 2a0. Getting the export in the first place

Most files that fail do so before any of the rules below are reached, because
the wrong download was taken. What to ask for:

| | |
|---|---|
| Where | the campaigns view, **download → More options**, not the one-click download |
| Segment | **daily** — one row per campaign per day. A summary with one row per campaign has no series in it |
| Format | **`.csv`**, never **`.csv (Excel)`** |

**The Excel option is not a CSV.** Measured on a real export: it is UTF-16
little-endian (BOM `ff fe`) and **tab**-separated, despite the `.csv` name. It
hits two guards in turn — first `whyOneColumn` ("probably tab-separated"), and
if only the separator is fixed, then the UTF-8 header check. Neither message
names UTF-16 specifically; if that proves confusing in practice, that is the
message to improve, not the guards.

Converting one rather than re-downloading works and is tested:

```bash
iconv -f UTF-16 -t UTF-8 "Campaign report.csv" | tr '\t' ',' > fixed.csv
```

On a 16,485-row export this produced a file that imported cleanly: 1,099 days,
15 campaigns, names containing commas intact, because those arrive quoted and
the quoting survives. A campaign name containing a **tab** would break it.

## 2a. How a campaign export is read

Real exports (Google Ads) carry **one row per campaign per day**: 15 campaigns
over 262 days arrives as 3,930 rows. That shape drives most of the design.

| What | What happens |
|---|---|
| Repeated days | Not an error. The column whose distinct values match the rows-per-day is the campaign column (override with `-by`). |
| Each campaign | Forecast on its own series. |
| The account | The sum of every campaign per day, forecast as its own series. `(account)` is its name. |
| Uneven rows per day | **Refused.** A day missing a campaign would put a step in the totals that never happened. |
| Text columns | Stored, never forecast. |
| `Campaign ID` and similar | Numeric, but a label. Stored, never forecast — adding fifteen together gives 327,129,489,016. |
| Rates (CTR, conversion rate) | **Forecast like anything else.** Percentages parse as numbers ("4.20%" -> 4.20, kept as written) and the sign goes back on in the report. What a rate is *not* is addable, so the account figure is the **mean** across campaigns, not the sum. |
| Settings (budget, bid, target CPA, caps) | **Stored and aggregated, never forecast.** A budget is a dial you turn; forecasting it just replays the number you set — the real file produced seven days of 5877.00. Cost per acquisition is *not* a setting: it is cost divided by conversions, an outcome you measure. |
| Paused campaigns | Every metric constant, so there is nothing to forecast. Stored, not sent to a model, and **named both in the terminal and in the report** — a campaign that simply vanishes from the page reads as an omission. Asking for one by `-entities` says why rather than "no campaign named". |
| Every original row | Kept verbatim in the `raw` table. |

The account and the campaigns are forecast **independently**, so their totals
will not match exactly. On the real file they agree to within 1–4%, which is a
useful sanity check rather than a guarantee.

## 2b. What a first run looks like

Verified by cloning the published repository onto a clean path and installing it
with no fixes applied. Three things look like faults and are not, so do not
"repair" them:

| What appears | Why |
|---|---|
| `Loading weights: 0%\|...` on every model command | the model library, on **stderr**. stdout carries the JSON protocol, so nothing else may print there. `2>/dev/null` gives clean output. |
| Reports and `pm.db` created `0600` | they name real campaigns and spend. Sharing stays deliberate (§4a). |
| A Hugging Face "unauthenticated requests" warning | expected for a public model (§2). |

Running the tool leaves generated files in the folder — a report per forecast and
`pm.db`. All are gitignored. To clear them:

```bash
find . -name '*_forecast_*.html' -not -path './models/*' -delete
find . -name 'pm.db*' -o -name 'walkthrough.db*' | xargs rm -f
```

`find`, not `rm *.html`: zsh fails a whole command when a glob matches nothing
and bash does not, and macOS defaults to zsh. Deleting `pm.db` discards every
stored forecast, which is what `accuracy` scores against.

## 3. Repository map

```
main.go          commands: setup, models, import, forecast, runs, accuracy
import.go        the recurring job: data/ -> forecasts -> two reports -> imported/
compare.go       the multi-model comparison report
compare_template.go  its page, with the campaign and metric dropdowns
ingest.go        CSV -> Data (dates, numeric columns, skipped text columns)
worker.go        the model protocol + output validation   <- read this first
db.go            SQLite: raw, series, runs, forecasts
report.go        builds the HTML report, draws the SVG charts
template.go      the report page, with htmx embedded via go:embed
format.go        number formatting for terminal and report

models/
  timesfm3_worker.py   ~70 lines: load the model, answer requests
  chronos2_worker.py   ~85 lines: same, plus covariate support
  weights_check.py     recomputes the weights sha256 before use
  fetch.py             downloads weights at pinned revisions
  requirements.txt     pinned, verified working together
  .venv/  cache/       created by install.sh, not in the repo

examples/
  01-simple.csv  02-marketing.csv  03-platform-export.csv  04-with-budget.csv
  05-campaigns.csv     several campaigns per day: the per-campaign + account case
  walkthrough.sh       every normal use, run for real
  ground-truth.py      proves stored numbers are the library's, unaltered
testdata/        fixtures, including deliberately broken workers
dist/            prebuilt binaries for people without Go
guidelines/      the two coding-style documents this project follows
```

## 4. The one thing not to break: the model protocol

Go starts a Python file as a subprocess and exchanges **one JSON object per
line** over stdin/stdout. `worker.go` holds the Go side.

**Startup.** The worker's first line declares what it is and what it can do. Go
never hardcodes model capabilities; it asks. The line is stored verbatim on every
run, which is how a saved forecast can name what produced it.

```json
{"model":"timesfm3","repo":"google/timesfm-3.0-pytorch","revision":"43046b85ec...",
 "weights_sha256":"a7592b0a...","covariates":false,
 "quantiles":[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9],
 "versions":{"timesfm":"3.0.2","torch":"2.14.0","numpy":"2.4.6"}}
```

**Request.** `series` is `[metric][day]`. Both models are multivariate and
forecast every metric in one call.

```json
{"id":"1","series":[[...],[...],[...]],"metrics":["spend","impressions","clicks"],
 "horizon":7,"quantiles":[...],"past_covariates":{},"future_covariates":{}}
```

**Reply.** `quantiles` is `[metric][day][quantile]`.

```json
{"id":"1","quantiles":[[[...]]]}
```

**Error.** `{"id":"1","error":"..."}`

Three rules on this seam:

1. **stdout belongs to the protocol.** Workers take the real stdout and redirect
   `sys.stdout` to stderr, because a progress bar printed by a library would
   otherwise corrupt the JSON stream. This has already happened once.
2. **A worker file must not be named after a Python package.** A script's own
   directory goes first on `sys.path`, so naming a worker after the `timesfm3`
   package shadowed the real one and broke the import. Hence the `_worker.py`
   suffix on every worker.
3. **Capability mismatch fails loudly.** Sending covariates to a model that
   declares `covariates: false` is an error, never a silent drop.

### Adding a model

1. Write `models/<name>_worker.py` (copy an existing one).
2. Add one line to the `models` map in `worker.go`.

**No other Go file may change.** If one has to, the design has sprung a leak —
that is the actual test in the review checklist.

## 4a. What the database holds

| Table | One row per | Why |
|---|---|---|
| `raw` | input row, as a JSON object of every column verbatim | the history to go back to for ad-hoc questions; keeps text columns and per-campaign detail |
| `series` | series_id + entity + metric + day | the numbers that were fed to the model |
| `runs` | forecast run | model, revision, weights sha256, metrics, entities, input hash |
| `forecasts` | run + entity + metric + day + quantile | the answer, at full precision |
| `forecast_accuracy` | **view**, not a table | joins each stored forecast to the actual that arrived later |

`raw` is replaced per source on re-import, so correcting an export does not leave
stale rows. Everything else is append-only except `series`, which updates in place.

The one index that is not a primary key is `forecasts_median`, a partial index on
`(entity, run_id, metric, day) WHERE quantile = 0.5`. It exists because
`forecast_accuracy` ends `WHERE f.quantile = 0.5` and quantile is the *last*
column of the forecasts primary key, so without it every query through the view
scans the whole table: 1.6s on a 2.3M-row file, against 12ms with it. If a plan
ever shows `SCAN forecasts` for a view query again, this index stopped applying —
`sqlite_test.go` asserts the plan, not the timing.

**How the file is opened** (`openDB`), all of it load-bearing:

| Setting | Value | Why |
|---|---|---|
| `journal_mode` | WAL | a reader and a writer at once; persists in the file, set once ever |
| `busy_timeout` | 10s | a blocked writer waits instead of dying with SQLITE_BUSY |
| `foreign_keys` | **on** | `forecasts` declares `run_id REFERENCES runs(id)`; SQLite defaults the pragma off, which left that reference decorative for most of this project's life |
| `synchronous` | FULL (default, deliberate) | writes once per forecast, not latency-bound, so there is nothing to buy by weakening durability |
| `user_version` | `schemaVersion` | see below |
| file mode | 0600 | it holds the account's whole spend history, and the default umask leaves it readable by everyone on the machine |

`busy_timeout` and `foreign_keys` are per-connection, so they are set in the DSN,
where the driver applies them to every connection rather than only the first.

**`CREATE TABLE IF NOT EXISTS` is not a migration.** On a file that already has
the table it ignores the new definition completely, without error. That is not
theoretical: this repo's own `pm.db` predated per-entity storage, every statement
appeared to succeed against it, and `accuracy` then failed with a bare
`no such column: entity`. `checkSchema` now gates every open — it stamps
`user_version`, adopts a current-shaped file that simply predates stamping, and
refuses one it cannot read with an explanation and a way out. Bump
`schemaVersion` and teach `hasCurrentShape` the new columns whenever a table
changes shape.

**Forecasts are kept so they can be scored later.** Each run records `as_of` — the
last day of real data it was based on, which is not always the day it was run.
The `forecast_accuracy` view joins a forecast to the actual for the same
series/entity/metric/day; `actual` is NULL until that day arrives and a newer
export is imported, at which point the same row fills in by itself.

It is a **view on purpose**. Every number in it already exists in `forecasts` and
`series`; copying them into a third table would only let the copies drift.

```bash
predictmarketing accuracy -db pm.db                  # all metrics, account level
predictmarketing accuracy -metric Cost -by-day       # how it decays with horizon
predictmarketing accuracy -entity "Brand"            # one campaign
```

Measured on the real Google Ads file with four weekly backtests: day-1 error 3.5%
(timesfm3) to 8.4% (chronos2), day-7 error 27-29% for both. The account is easier
to forecast than any single campaign, which is aggregation smoothing noise.

Ad-hoc queries use SQLite's json1:

```sql
SELECT day, json_extract(data,'$.Campaign'), json_extract(data,'$.Cost')
FROM raw
WHERE source='google-ads' AND json_extract(data,'$."Campaign status"')='Enabled';
```

## 4b. How a column is classified

In order. The first rule that matches wins, and whichever applied is printed.

| Rule | Test | Result |
|---|---|---|
| Identifier | last **word** is `id`, `ids` or `code` | stored, never forecast |
| Setting | contains `budget`, `bid`, `target`, `limit`, `cap` | stored and aggregated, never forecast |
| Rate | contains `ctr`, `rate`, `%`, `ratio`, `share`, `avg` | forecast; **averaged** across campaigns, not summed |
| Anything else numeric | — | forecast, summed across campaigns |
| Not numeric | — | stored in `raw`, never forecast |

`TestColumnClassification` pins every real Google Ads column name to its bucket.
Two traps it exists to catch:

- **"Max CPC bid" ends in "id"**, so a suffix test read it as an identifier and
  the settings rule never saw it. Match the last word, not a suffix.
- **CPA is not a setting.** It is cost divided by conversions, an outcome you
  measure. `Target CPA` is a setting; `CPA` and `New customer CPA` are not.

## 4c. The fine-tuned model

`chronos2ft` is the pinned Chronos-2 weights plus a small LoRA adapter trained by
`models/finetune.py`. The base weights are never modified.

**It cannot exist after a fresh install.** It is trained on the user's own data,
and a fresh install has none. `install.sh` installs everything it needs (`peft`,
`accelerate`) and its closing message says how to train it. Until then
`predictmarketing models` reports it as unavailable with that same instruction —
that is the expected state, not a fault.

The order is: install -> forecast with `chronos2`/`timesfm3` -> train the adapter
on a CSV you now have -> `chronos2ft` becomes a third option.

```bash
models/.venv/bin/python models/finetune.py "Campaign report.csv" --steps 2000
./predictmarketing forecast "Campaign report.csv" -model chronos2ft
```

Training writes `models/finetuned/chronos2ft/` (4.9 MB) and records what it was
trained on in `models/finetuned.json`. The worker checksums the adapter before
use, exactly as the pretrained models checksum their weights.

The registered path is **relative to `models/`**, so moving the project does not
break it. `share.sh` excludes both the adapter and the registry: it is fitted to
one person's numbers and registered on their machine, so sending it would give
someone a broken or simply wrong model. `models/finetune.py` does travel, so they
can train their own.

**Measured on the machine in the README (M1 Pro, 32 GB), full fine-tuning vs LoRA:**

| | full | lora |
|---|---|---|
| steps in 242 s | 607 | 720 |
| peak memory | 3.31 GB | 3.31 GB |
| checkpoint | 456 MB | **4.9 MB** |

Full fine-tuning is perfectly feasible here — LoRA is only 19% faster and uses the
same peak memory at batch 8. LoRA was chosen for the 99x smaller checkpoint, which
makes keeping a history of them practical.

**Fine-tuning did not help.** On a held-out test (train to 14 days before the end,
score the 7 days after), stock Chronos-2 scored 32.7% MAPE; full fine-tuning 34.6%
and LoRA 34.5%. Seven series is far too little for a model pretrained on millions.
Treat `chronos2ft` as an experiment and **score it before trusting it**.

### The leakage guard, which is not optional

A fine-tuned model has already seen the days it was trained on. Scoring it against
them measures memorisation: measured on this data it gets 9-14% MAPE on memorised
days while stock Chronos-2 honestly gets 10-19%. It looks better and means nothing.

So the worker declares `trained_through` in its handshake, that is stored verbatim
in `runs.model_info`, and `forecast_accuracy` exposes a `trained_on` flag:

```sql
-- always, for any accuracy question
WHERE actual IS NOT NULL AND trained_on = 0
```

The `accuracy` command applies it and prints how many rows it excluded. Any new
query you write must filter it too.

## 5. Hard rules

These are not style preferences. Each one exists because its absence produced a
wrong answer that looked right.

- **Never silently ignore input.** Unusable input is named in an error. Nothing is
  skipped, defaulted, or quietly dropped.
- **Never fabricate data.** Covariate history must come from a real CSV column.
  Inventing one (zeros) was tried and measurably fed the model noise: it widened
  the interval while leaving the median unmoved across a 100,000x range of inputs.
- **Never add up what cannot be added.** Identifiers are labels; rates are means,
  not sums. Store everything, and say which rule was applied to each column.
- **Never forecast something you control.** Budgets, bids, targets and caps are
  inputs, not outcomes. Store them, aggregate them, leave them out of the model.
  They are still valid `-future` inputs -- a budget is the textbook known-future
  value, because you chose next week's yourself.
- **Never score a fine-tuned model on days it was trained on.** Filter
  `trained_on = 0`. See §4c.
- **Never derive one metric from another.** Every metric is forecast by the model
  itself. No ratios, no percentages. `multivariate_test.go` proves this with three
  unrelated shapes and a perturbation test. Keep those tests.
- **Validate every forecast before storing it**: right shape, no NaN or infinity,
  quantiles ascending. A failing forecast fails the run; nothing is written.
  Negative values are legitimate and are *not* rejected.
- **Quantile crossing is repaired, not rejected.** Both models predict each
  quantile independently, so mild crossing (~0.2%) is expected. It is fixed by
  sorting — monotonic rearrangement, a standard and strictly improving correction —
  and the size of the largest correction is reported. Crossing beyond 5% is refused.
- **Day/month order is decided once per file**, never per row. A file where nothing
  settles it is refused, because guessing shifts every date by up to eleven months.
- **Resolve paths against the binary**, not the working directory (`installDir`).
- **Mark deliberate shortcuts** with a `ponytail:` comment naming the limit and the
  upgrade path.

## 6. Settled decisions — do not reopen

| | |
|---|---|
| Language | Go for everything except ~190 lines of Python adapter. Not "100% Go" — earlier drafts of these docs said so and were wrong. |
| Storage | SQLite via `modernc.org/sqlite` (pure Go, no CGo, so it cross-compiles) |
| Dependencies | That one, and nothing else. Standard library for the rest. |
| Output | A static HTML file you double-click. **No server. No `serve` command.** |
| Report name | Includes the model, so two models do not overwrite each other |
| Front end | htmx `go:embed`-ed and inlined into the page. Never a sibling file or CDN — those dangle the moment the report is moved. Charts are hand-drawn inline SVG. |
| Forecasting | Multivariate: every numeric column in **one** model call. Never loop per metric; that throws away the joint signal both models are built to use. |
| Horizon | Defaults to 7 for both models. |
| Running models without Python | **Tested and rejected.** See `FINDINGS-onnx.md`. |

## 7. Measured facts (do not re-derive or guess)

- A forecast takes **~3 seconds** whether the horizon is 7 days or 400 — almost all
  of it is loading the model. Hence the 5-minute timeout is ~100x headroom.
- Python side installed: **~800 MB** (torch is 558 MB of it). Weights: **1.7 GB**.
- A clean `./install.sh` measured **182 seconds** on a fast connection.
- ONNX: Chronos-2 has **no path** — `torch.export` cannot capture it because it
  branches on whether the input contains NaN, and that handling is real semantics.
  TimesFM 3.0's core transformer **does** export correctly (relative diff 2.19e-06)
  with one custom op, but is frozen to a fixed context length and `predict()`
  orchestrates everything else in Python. Do not re-run these experiments.

## 8. Verifying a change

Before claiming a change works:

```bash
gofmt -l .                 # silent
go vet ./...               # silent
go test -count=1 ./...     # all pass
./predictmarketing forecast testdata/example.csv -model timesfm3 -horizon 3
./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 3
```

If you touched anything between the CSV and the database, **re-prove ground truth**:
call the Python library directly on the same matrix and compare to what was stored.
```bash
models/.venv/bin/python examples/ground-truth.py /tmp/gt.db
```

Every line must say `EXACT`. The stored numbers must equal the library's output
exactly, sorted only where the model's own quantiles crossed.

To see every normal use of the tool run for real: `./examples/walkthrough.sh`

## 9. Why the tests look the way they do

Every test maps to a defect that was actually found by attacking the tool, not to
a hypothetical. The ones worth knowing about, because they are easy to reintroduce:

| Defect | Symptom |
|---|---|
| Newest-first CSV read in file order | reversed series, confident nonsense |
| Campaign rows summed without excluding `Campaign ID` | an "account total" of 327 billion |
| A flat-zero series judged for quantile crossing | valid forecast refused as "10% out of order", when the values were 1e-8 |
| A percentage column read as text | `CTR` silently dropped from forecasting entirely |
| A budget forecast | seven days of the number you already set, presented as a prediction |
| A campaign named `(account)` | silently folded into the total and gone as an entity |
| `-entities` split on commas | a campaign called "Video, Infeed, CPM" could not be selected at all |
| Report built assuming the account is present | nil pointer when `-entities` picks only campaigns |
| `accuracy` filter matching nothing | empty table that reads like "no data yet" instead of "you typo'd it" |
| "Max CPC bid" classed as an identifier | a real budget setting escaped the settings rule |
| `-future` validated against forecastable columns only | classifying Budget as a setting broke `-future Budget=...`, the single most natural use of the flag |
| A fine-tuned model scored on its training days | 9-14% MAPE that measures memorisation, not skill |
| A missing day treated as continuous | every forecast date wrong |
| `01/02/2026` guessed as day/month | every observation shifted up to 11 months |
| Covariate history fabricated as zeros | feature added noise, not information |
| Quantile tolerance fixed at 1e-6 | valid forecasts rejected as "not ascending" |
| Weights sha recorded but never verified | provenance was a claim, not a check |
| Report referenced an htmx file it did not ship | broken page once moved |
| Both models wrote the same report file | comparing models destroyed one of them |
| `-model` ignored after the filename | ran a different model than asked |
| A database from before per-entity storage | `no such column: entity` from every command, saying nothing about why |
| `forecast_accuracy` filtering on the last column of a primary key | full table scan per query: 1.6s on 2.3M rows |
| `known()` validating filters against every quantile of every run | 1.16s spent on every `accuracy` call to check one name |
| `foreign_keys` left at SQLite's default of off | a declared reference enforcing nothing; a forecast could outlive its run |
| `ParseFloat` accepting "NaN" and "Infinity" | NaN surfaced as a raw NOT NULL constraint error, Inf stored silently and poisoned every sum, axis and average after it |
| Fuzz corpus containing "NaN" and "Inf" fragments | looked like coverage for three rounds; the files were too short to reach value parsing, so it tested nothing |
| Database left at the default umask | 0644, world-readable spend history |
| `-db ""` | SQLite opens an anonymous temporary database, so a forecast printed "saved run ... to " and stored nothing |
| `-db` pointing at a directory or a missing one | the driver reports "unable to open database file: out of memory", which says nothing about the typo that caused it |
| Treating an absent table as an old schema | a second process opening a new file mid-creation was intermittently told its database predated per-entity storage |
| Stamping `user_version` on every open | turned every read into a write, so `accuracy` failed outright on a database on read-only media |
| Doc and installer tests asserting that generated paths exist | passed in a working copy, failed in the bundle a recipient unpacks: ten failures before anyone could run `./install.sh` |
| Campaign totals added without a finite check | individually-finite values could sum to +Inf in the account series, which SQLite stores silently and every later total, axis and average inherits |
| The console summary indexing the account unconditionally | every `-entities` run that excluded the account panicked with "index out of range [0] with length 0"; the report template had been fixed for this, the summary beside it had not |

None of these were in the models. All were in the surrounding code.
