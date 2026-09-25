# AGENTS.md — Predict Marketing

Instructions for any AI agent working on this project (Claude, Codex, Gemini,
Qwen, Hermes, Cursor, or a human who likes checklists).

**This file is the single source of truth.** `CLAUDE.md` and `.claude/skills/`
point here rather than restating anything, so they cannot drift out of step.

---

## 1. What this is

A Go command-line tool that forecasts daily marketing numbers — spend,
impressions, clicks, and anything else numeric — for a whole account **and** for
each campaign in it, using one of three time-series models — two pretrained, and
one fitted to your own data:

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
go test ./...               # 172 tests and a fuzz target, 190 cases
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

Commands: `setup`, `models`, `import`, `report`, `forecast`, `runs`, `accuracy`,
`version`.

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
| Empty the database — but only for the first file of the run, and only once the CSV has parsed | `storedRuns`, then `clearDatabase` (§4a) |
| Forecast each model over every entity, **once per window**, writing nothing | `importWindows`, `lastDays`, `forecastModel` |
| Store the mean of the 90-day runs as a run of its own | `averageRun` |
| Report 1 into `data/reports/`: the average line alone | `reportPath`, then `writeComparison` |
| Move the CSV to `data/imported/` | `fileAway`, never overwrites |
| Reports go to `data/reports/`, never beside the export | `reportPath` — `data/` is meant to show at a glance what is still unread |
| Train `chronos2ft` on the **full** history, then report 2: the average plus `chronos2ft@full` | `trainFinetune`, then `writeComparison` again |

**The table is one file's job, and `cmdImport` stops at the first file that
fails.** `pendingFiles` sorts the folder's CSVs by name and calls `importOne` in
that order; the first error returns, so every file after it in the alphabet is left
untouched and unmentioned. Three files where the second is too short prints the
first file's full import, then the refusal, and nothing at all about the third —
which was fine and is still sitting in `data/`. The state afterwards is exactly the
prefix that succeeded. Re-running after moving the bad file out picks up where it
stopped, because a filed-away CSV is no longer pending.

**`chronos2ft` is retrained on every import**, on the newest data, which costs
a real stretch of CPU each time -- minutes rather than seconds. `--steps` sets
how many steps run, but **the cost per step is not constant**, so the file does
affect the total. Two runs on the same machine, both 2,000 steps, both
`batch_size 8` and `context 512`:

| Export | Series | Days | `train_seconds` |
|---|---|---|---|
| `examples/05-campaigns.csv` | 5 | 150 | 545.2 |
| a real Google Ads export | 7 | 1,099 | 1,041.6 |

Do not extrapolate a rate from two points; measure instead. Every run records its
own `train_seconds`, `steps` and `train_series` in `models/finetuned.json`, which
is the only figure worth quoting for a given machine and file.

Retraining every time is deliberate: an adapter fitted to last quarter's numbers
quietly goes stale, and a stale model that still looks current is worse than no
model. `-no-finetune` writes report 1 and stops, for when you only want the two
pretrained models.

**Training is never time-limited.** `models/finetune.py` once took a `--budget` wall
clock and `import` passed 600 seconds. On a real export that stopped training at
step 1,210 of 2,000 -- 0.605 of an epoch -- and produced an adapter that was
undertrained while appearing in every report as a peer of the pretrained models.
Nothing in the reports said so. The flag, the callback and the registry field are
gone. Do not reintroduce a time limit in any form: if training is too slow, lower
`--steps`, which is honest about what was asked for.

### Three windows, one chart

Every import forecasts the same file three times — the **whole file**, its **last
270 days** and its **last 90 days** — with both pretrained models. Six runs on a
file long enough for all three windows, fewer when a window is skipped. Every one
of them is **stored**; only the **90-day average** is **drawn**.

| Window | Runs when | Stored as |
|---|---|---|
| `full` | always | `chronos2@full`, `timesfm3@full` |
| `270d` | the file has **more than** 270 days | `chronos2@270d`, `timesfm3@270d` |
| `90d` | the file has **more than** 90 days | `chronos2@90d`, `timesfm3@90d` |

**What each report draws:**

| Report | Lines |
|---|---|
| 1 | actuals + `average@90d` |
| 2 | actuals + `average@90d` + `chronos2ft@full` |

`average@90d` is the mean of `chronos2@90d` and `timesfm3@90d`, per entity,
metric, day and quantile. The fine-tune is given the **whole file** — it is the
one model that learns from the data rather than reading it, and more of it is
what training has to work with.

**Why the 90-day window, and why only the average.** Measured by a walk-forward
backtest over **31 daily origins** (2026-08-24 to 2026-09-23), each forecasting 7
days blind on a real account:

| | account level | all entities |
|---|---|---|
| 90-day window | **17.40%** | **32.53%** |
| whole file | 18.88% | 33.91% |
| 270 days | 19.14% | 34.36% |

The 90-day window won **every one of the 7 horizons and the pooled total, at
both account and campaign level — 16 comparisons out of 16** — by about 1.7
points of mean absolute error. Choosing the *window* was worth roughly
2.5x more than choosing the *model* (0.70 points between the best and worst
model, pooled across windows). 270 days was the worst of the three, so this is
not a smooth "recent is better" gradient: it is that the last ~90 days are the
regime this account is actually in.

The average was the best single line at account level (17.33%, beating both
models), so it is what the report recommends. The individual model lines answer a
question the report is not asking, and six of them crowd out the one line that is
the recommendation — so they are kept in the database and left off the page.

**They are still stored.** Every window run goes into `runs`/`forecasts` under
`chronos2@90d`, `timesfm3@270d` and so on, because `accuracy` groups by that
column and re-running that comparison on real future days is how the choice above
gets re-tested rather than taken on faith.

**A window longer than the file is skipped, never refused.** A 100-day export
produces `full` and `90d` and says so. A window exactly as long as the file *is*
the file, so it is skipped too — running it again would store a duplicate run and
draw a second identical line. That means 90 days produces only `full`, and the
first length giving all three is **271 days**.

**Below 90 days nothing runs at all.** The import gate (`enoughHistory`) is the
one the reader is told about, even when the lower model floor
(`smallestUsefulSeries`, 32) fired first: `readCSV` returns a `tooShort` carrying
the day count, and `importOne` converts it. Quoting 32 at someone who needs 90
sends them back with a file that will be refused again.

**The window is part of the stored model name** — `chronos2@90d`, not `chronos2`
— because `accuracy` groups by that column, and the entire point is to find out
which history length forecasts best. `runLabel` builds it; the worker is still
started by the bare model name. Nothing validates `runs.model` against the worker
registry after a run, which is what makes this possible.

**Every window forecasts the same campaigns and metrics.** `lastDays` trims the
numbers and nothing else — `Names`, `Entities`, `GroupBy` and the exclusion lists
stay as the whole file decided them. Otherwise a shorter window could classify a
campaign differently, and `writeComparison` intersects entities across runs, so
that campaign would vanish from the chart without a word.

**`average@90d` is built from the 90-day window only**, and excludes
`chronos2ft`. The fine-tune is fitted to the same data it would be averaged into
and has not beaten the stock models (§4c), so including it would let a weaker,
leakier opinion pull the ensemble. It also excludes any run declaring a different
quantile grid: averaging a q0.1 with a q0.05 produces a number belonging to
neither.

**On a file of exactly 90 days the average falls back to `full`.** The 90d window
is skipped there as a duplicate of the whole file, so there would be no 90-day
runs to average — and `full` *is* the last 90 days in that case. Without the
fallback the one file length that is exactly the documented minimum would produce
no line at all. `TestTheAverageFallsBackToFullOnAnExactlyMinimumFile` pins it.
The label does **not** change: it is still stored as `average@90d`, because
`averageLabel` is a constant. On that one file length the name is accurate
anyway, but do not read the `@90d` in `runs.model` as proof of which window fed
it — check which runs the `averaged` field of its `model_info` lists.

**Why two reports.** The third model has to be trained on the user's own data
first, which takes as long as it takes. Report 1 is written and the CSV filed away
*before* training starts, so a fine-tune that fails leaves a completed import
and a readable report rather than nothing.

**Where an interrupted import leaves things.** The order in `importOne` is the
recovery map, because each step is the only thing that records itself:

| Stopped | `data/` | `runs` | `series` / `raw` | Reports |
|---|---|---|---|---|
| reading the CSV, or the history gate | CSV still there | untouched — the wipe has not happened yet | untouched | — |
| a model, part way | CSV still there | the windows already finished; nothing for this one | already written | — |
| after the last window, before the average | CSV still there | one row per model per completed window, no `average@90d` | already written | — |
| the fine-tune | CSV in `imported/` | every window run **and** `average@90d` | written | report 1 only |

A `runs` row appears only once a model has finished every entity, so a model killed
half way leaves nothing behind. `series` and `raw` are written *before* any model
runs, so **history in the database is never evidence that a forecast happened** — a
Ctrl-C during the first model still leaves the whole export stored.

**What the interruption cannot undo is the wipe.** From the moment the first file
parses, whatever the database held before this import is gone (§4a), and stopping
part way does not bring it back.

Re-running `import` on a CSV still in `data/` is the intended recovery, and it is
safe because that wipe happens again: the half-finished runs go with it, so the
second attempt leaves exactly one set. Nothing is double-counted.

Once the CSV has moved to `imported/` the job cannot be re-run that way without
re-forecasting both pretrained models over every window and filing a second copy of
the same export. The `finish-an-import` skill has the steps that finish it in place.

**A failed fine-tune exits 0.** `importOne` returns `nil` on every fine-tune failure
path, so a cron job or script sees success and one report. The only reliable check
is whether `data/reports/<name>_with-finetune.html` exists — and a stale one from an
earlier import is never cleaned up, so check its timestamp too.

**The comparison report** (`compare.go`, `compare_template.go`) is separate from
the single-model report in `report.go`, because it answers a different question:
not "what does this model say" but "do the models agree". Every drawn line's
median goes on one set of axes over the same history, **with its q10-q90 range
shaded behind it** at 13% opacity, inside the same group so the legend hides a
line and its band together.

The band was deliberately absent for most of this project's life, and the reason
was good at the time: the page drew six model lines, and six overlapping
translucent bands are unreadable. It draws one or two now, so the interval
is legible — and it was always in the database, since every worker returns nine
quantiles and only the median was ever rendered. A model that declares a single
quantile gets no band rather than a degenerate one.

The lede adapts: one line describes the band, several describe the disagreement
between them. Saying "1 models forecast ... where the lines separate" is how that
copy read for an hour after the report went down to a single line.

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

One caveat, because it decides what you are allowed to change: the crosshair does
**not** in fact depend on the width being fixed. `fromEvent` multiplies the pointer
offset by `svg.viewBox.baseVal.width / rect.width`, so it already survives any
uniform scaling, and the markers and the rule are positioned in user units and
scale with it. Forcing `width:100%` on a real report — scale factor 5.25 — left the
crosshair reading the correct day and the correct values. What the fixed width buys
is **legibility**: 150 history days at 9px plus 7 forecast days at 46px need
1789px, and squeezed into a 341px column the whole forecast is 60px wide with every
line, marker and label on top of the others. So do not scale it — but if something
does scale (a print stylesheet, a container query, a phone), the crosshair maths is
not what breaks, and rewriting it is wasted work.

**The crosshair and the table below it do not share a formatter.** The stat cards
and the table are rendered in Go by `formatMetric`, which chooses precision from
magnitude and puts the `%` back on a rate. The readout is rendered in the browser
by `money()`, which adds thousands separators, uses `toFixed(2)`/`toPrecision(3)`,
and knows nothing about percent — the JSON carries `xs`, `days`, `actual`, `cut`,
the y projection and the raw values, and no percent flag at all. So one day of one
model reads `22,855` in the readout and `22855.04` in the table directly underneath,
and a rate reads `4.2` above and `4.200%` below. Changing how numbers print means
changing **both**, and giving the readout its `%` back means adding the flag to the
JSON first.

Each series is drawn inside `<g class="series" data-model="...">`, and the end
labels inside `<g class="series-label" data-model="...">`, so the legend hides a
line, its markers and its label with one selector and no knowledge of the
drawing. The **readout sits above the chart, outside the scroller**: the first
version put it inside, where it scrolled away with the content and showed
nothing, while a synthetic mousemove in a test found it perfectly.

**Only the pane visible when the page loads opens on the forecast.** The
scroll-to-the-right happens once, in a `requestAnimationFrame` at the end of the
per-pane setup loop. By the time it runs, `refreshMetrics()` has hidden every other
pane, and a `display:none` element reports `scrollWidth === 0`, so the assignment is
a no-op for all of them; `show()` never re-scrolls. Every pane reached through the
dropdowns therefore opens at the **oldest** day with the forecast off-screen to the
right. Measured on a real 15-pane report: the pane visible at load sat at
`scrollLeft` 643 of a maximum 643; switching the metric dropdown revealed a pane at
0 of the same 643. If you fix it, the fix belongs in `show()`, not in the
`requestAnimationFrame`.

`spreadLabels` separates the end labels **from each other only**. They share the
right-hand margin with the y-axis tick labels and nothing deconflicts the two: a
tick is drawn at `w-padR+12` and an end label at `xs[n-1]+8`, four pixels apart,
both anchored at the start. Whenever a model's last forecast value lands within
about a line-height of one of the five grid values, its name prints on top of that
number and both become unreadable — 5 of 15 panes on a real report. It is a function
of the data, so it will not reproduce on a test fixture. If you touch either label,
check several panes, not one.

`-history 0` (the import default) draws every day there is. Scrolling back is
only useful if there is something behind you.

The page is dark and monospaced deliberately: it is dense and numeric, a tabular
font keeps columns of money aligned, and a fixed palette means a screenshot looks
the same to everyone. Line identity is carried by **dash pattern as well as
colour** (`dashFor`/`strokeFor`), so the chart survives greyscale and colour
blindness. End labels are pushed apart by `spreadLabels`, because models that
agree finish at the same height and would otherwise print their names on top of
each other exactly when the chart is most worth reading.

### Redrawing the reports: `report` (`rerender.go`)

A report is a rendering of numbers the database already holds, so a change to how
a chart is drawn should not cost a forecast — let alone the retrain, which is
measured in minutes and would give different numbers to look at. `report` rebuilds
the pages from the runs already stored.

It is **read-only, deliberately**. No CSV is read, nothing is forecast, nothing is
trained, and nothing is written to the database. That is the whole point of it
being a separate command rather than a flag on `import`.

| What it does | Where it lives |
|---|---|
| List the datasets that have forecasts, or check the one named | `storedSeries` |
| Take the newest run of each model, **restricted to the newest `as_of`** | `latestRuns` |
| Narrow those to the lines `import` draws: `average@90d`, plus any fine-tuned run | `rerenderSeries` |
| Read the stored quantiles back into `[metric][day][quantile]` | `loadForecast` |
| Rebuild the history and the inactive entities from `series` | `rebuildData` |
| Write report 1, and report 2 if a fine-tuned run is stored | `writeComparison` |

**The narrowing is what keeps a redraw from disagreeing with the page it
replaces.** The window runs are all there under the newest `as_of`, and drawing
them would put six lines on a page `import` drew one line on. So `rerenderSeries`
keeps `average@90d` alone for report 1 and adds the fine-tuned run for report 2.
A database written before the average existed has no `average@90d`, and there it
falls back to every pretrained run rather than producing an empty page — which is
also the path a bare `forecast` takes, since `forecast` stores `chronos2`, not
`chronos2@90d`.

Only runs sharing the newest `as_of` are drawn. Two runs made from different
amounts of history are not comparable, and putting them on one set of axes would
invent a disagreement that is really a difference in what each model was shown.
A dataset whose only runs are pretrained gets report 1 alone; the second report
appears when a run declares `trained_through` (§4c).

**`runs` cannot show you which runs those are.** Its `WHEN` column is
`created_at` — when the command was typed — and `as_of`, the last day of real
data the run was given, is not printed at all. The two come apart the moment you
backtest or re-forecast an older export. So after a single `forecast` on a newer
file, `report` will silently draw a **one-model** comparison page (it names the
models it used on the line it prints) until every model has been run at the same
`as_of`. `import` never hits this: every run it makes shares one `as_of`, and the
narrowing above puts the redraw back on the lines `import` drew. To see what
`report` will pick:

```sql
SELECT substr(id,1,12), series_id, model, as_of, horizon, created_at
FROM runs ORDER BY as_of DESC, created_at DESC;
```

**Mixed horizons under one `as_of` produce a page that overstates one model.**
Nothing refuses them: a 7-day `import` plus a 14-day `forecast` on the same
series draws an axis out to the 14th day with the 7-day model still on it. Give
every model the same `-horizon`, or the comparison is not one.

The file names are `reportPath`'s, the same ones `import` writes, so `report`
replaces the pages in place rather than leaving a second set beside them.

**`report` redraws the comparison pages only.** `rerenderSeries` calls
`writeComparison`; nothing in `rerender.go` calls `writeReport`. The single-model
page — `report.go`, `template.go`, its SVG, its q10/q50/q90 table, its provenance
block — is written from exactly one place, `cmdForecast`, and is produced by neither
`import` nor `report`. So **if you change `report.go`, `template.go` or `format.go`,
running `report` proves nothing**: it will happily rewrite `data/reports/*.html`
with none of your change in them. Re-run `forecast` instead, and give it a scratch
database — `forecast` calls `saveRun` before `writeReport`, so every look at a
drawing change stores another run, and those runs are all scored later against the
same actuals:

```bash
./predictmarketing forecast examples/05-campaigns.csv -model chronos2 \
  -db /tmp/scratch.db -out /tmp/r.html
```

`report` is read-only precisely so that iterating on the *comparison* drawing is
free; the single-model report has no such path, and `-db` is what keeps it out of
the real history.

### How numbers are printed (`format.go`)

Precision is chosen **per value from its magnitude**, never per column:

| `abs(v)` | rendered |
|---|---|
| exactly 0 | `0` — this also normalises the `-0.00` the models return |
| `>= 1e15` or `< 1e-4` | `%g` to 6 significant figures, i.e. scientific |
| `< 1` | 6 decimal places |
| `< 100` | 3 decimal places |
| otherwise | 2 decimal places |

Two consequences that look like bugs and are not. **The decimal count changes row
to row inside one column** — a `Clicks` column crossing 100 prints `99.885` then
`107.63`; `tabular-nums` aligns glyph widths but cannot align differing digit
counts. And **`compact()`, which draws the y-axis, rounds to integers below 1,000
and is shared with the comparison page**: any metric whose whole range sits under 1
gets an axis of five `0`s. Measured on a generated rate file: a conversion rate
around 0.031 drew the labels `0 0 0 0 0` while the table beside it read `0.031097`;
a CTR around 4.5 drew `4 5 5 5 5`. The curve and the table are right; the axis is
the part saying nothing. `compact()` also never carries the `%`, so a rate's axis
and its table are in different units on the same screen. Fixing it changes both
pages.

**Two smaller things a redraw cannot reproduce exactly.** The excluded-campaign
note comes out in a different order — `import` lists them in file order,
`rebuildData` derives the list from `series` with `ORDER BY entity` and has no
file order to recover. And the generation timestamp differs whenever the redraw
lands in a later minute — it is printed to the minute, so a redraw run straight
after the import usually carries the same one. Both are cosmetic. They are why a
byte-for-byte diff of the two pages is not a *reliable* check, even though on
`examples/05-campaigns.csv` it does come out identical (§9); compare the drawn
lines instead.

**One thing is not recoverable: the `%` sign.** `series` stores the number a rate
was parsed to, not that it was written as a percentage, so `data.Percent` is empty
on a rebuild and a re-rendered rate prints bare. The number is right; only the
sign is missing. Re-importing the export restores it. Do not fix this by guessing
from the column name — the classification rule (§4b) decides what a rate *is*, and
whether the file wrote a sign is a separate fact that would have to be stored.

**A second thing is not recoverable, and it is worse: why an entity is absent.**
`import` gets its "switched off in the export" list from the export's status column.
`rebuildData` has no export — it derives the list by set difference between the
entities in `series` and the entities the runs forecast. Those are not the same
question. A campaign that was simply dropped from a later export, or renamed, is
labelled `switched off in the export` on the redrawn page, and told its history is
"still counted in the account total", which is also false on that path. Reproduced
by importing Alpha/Beta/Gamma then Alpha/Beta and redrawing. The page is right
whenever the runs and the `series` rows come from one import; it is wrong the moment
they do not. If the exclusion note matters to you, re-import rather than redraw.

`forecast` flags:

| | |
|---|---|
| `-model` | `chronos2` (default), `timesfm3` or `chronos2ft` |
| `-horizon N` | days ahead, default 7 |
| `-history N` | days of past data drawn on the chart, default 90. **`0` here means 90, not "all"** — `drawChart` clamps anything `<= 0` back to the default, unlike `import` and `report` where `0` means every day. To draw the whole history from `forecast`, pass a number larger than the file (`-history 100000`). |
| `-columns A,B` | which columns to forecast (commas) |
| `-entities A;B` | which campaigns (**semicolons** — campaign names contain commas) |
| `-by NAME` | column separating campaigns, if it cannot be worked out |
| `-future K=v,v` | known-future values for a column |
| `-series NAME` | dataset name, default the file name |
| `-db` / `-out` | database and report paths |

**`-future` is one set of values, sent to every entity.** The past history of a
covariate is read per campaign (`data.Values[entity]`), but the future you type is
a single map handed unchanged to every request — the account and each campaign.
On a campaign export the default `-entities` is *all of them plus the account*, so

    forecast "Campaign report.csv" -future "Budget=900,900,900,900,900,900,900"

tells a campaign whose budget has been 40/day for its whole history that its next
seven days are 900. Nothing says so: the summary prints one line,
`using known-future: Budget`, with no per-entity qualification, and the forecast
completes normally. Measured on `examples/05-campaigns.csv`, where `Brand Search`
has a flat `Budget` of 40.00 and accepted `-future "Budget=5000,5000"` without
comment.

Pair `-future` with `-entities` so the number you know belongs to the thing you
are forecasting. There is no way to give a different future per campaign in one
run: forecast them one at a time, or leave `-future` off.

**`-columns` narrows the model call only.** Every numeric column is still read,
still aggregated and still written to `series` and `raw`; `-columns` picks which
of them are sent to the model. Omitted metrics keep supplying actuals for later
scoring, and they are named in no line of the summary — so a column missing from
`forecasting:` does **not** always mean the classification rule (§4b) dropped it.
Check what you passed before reading it as a bug.

`import` flags:

| | |
|---|---|
| `-data DIR` | folder to read from, default `data` |
| `-horizon N` | days ahead, default 7 |
| `-history N` | days of past data drawn on the charts, default 0 = all of it |
| `-no-finetune` | write report 1 only, skipping the retrain |
| `-db FILE` | database file, default `pm.db` |

`report` flags:

| | |
|---|---|
| `-series NAME` | which dataset, default every one that has forecasts |
| `-history N` | days of past data drawn on the charts, default 0 = all of it |
| `-data DIR` | folder whose `reports/` to write into, default `data` |
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
| **Date range** | **must end on yesterday**, the last full day of ad spend. Never include today |
| Format | **`.csv`**, never **`.csv (Excel)`** |

**The date range is the one that silently corrupts the forecast.** A day still
in progress is a partial day, and nothing in this tool can tell it from a real
collapse — `parseCell` sees a smaller number and stores it, the guards check
shape and finiteness rather than plausibility, and both models weight the newest
days heavily. One short day at the end therefore moves every forecast after it.

Measured, same calendar day, two downloads of the same account:

| 2026-09-23 | taken mid-afternoon | taken once complete |
|---|---|---|
| Cost | 4,435.52 | 6,378.35 |
| Impr. | 10,957 | 17,723 |
| Clicks | 839 | 1,332 |

The partial day was 81% of the prior week's median spend, 68% of impressions and
65% of clicks. Forecasting from it put the next 7 days **28% low on Cost and 37%
low on impressions**, on both models — they moved together, which is what makes
it look like a finding rather than an error.

A cheap sanity check before importing, which is also the one `importOne` does
not do:

```bash
awk -F, 'NR>1{c[$1]+=$10} END{for (d in c) print d, c[d]}' "Campaign report.csv" \
  | sort | tail -8
```

`$1` is the day and `$10` the cost column — **check both against your own
header first**, they move with the export (`Cost` is field 6 in
`examples/05-campaigns.csv`). `awk -F,` also splits inside quotes, so a campaign
name containing a comma shifts every field after it on that row; that skews the
daily totals but not the shape this check is looking for.

If the last day is far below the ones before it, the export ran too early.
Re-download ending on yesterday. **The tool does not check this and should not
be assumed to** — there is no way to distinguish a partial day from a genuine
one without knowing when the file was produced, which the file does not say.

**The Excel option is not a CSV.** Measured on a real export: it is UTF-16
little-endian (BOM `ff fe`) and **tab**-separated, despite the `.csv` name. It
hits two guards in turn — first `whyOneColumn` ("probably tab-separated"), and
if only the separator is fixed, then the UTF-8 header check. Neither message
names UTF-16 specifically; if that proves confusing in practice, that is the
message to improve, not the guards.

Converting one rather than re-downloading works, and was done on the real export:

```bash
iconv -f UTF-16 -t UTF-8 "Campaign report.csv" | tr '\t' ',' > fixed.csv
```

On a 16,485-row export this produced a file that imported cleanly: 1,099 days,
15 campaigns, names containing commas intact, because those arrive quoted and
the quoting survives. A campaign name containing a **tab** would break it.

**The numbers have to be written the English way.** `parseCell` strips `$ £ €`,
spaces and commas and hands what is left to `ParseFloat`, so a file written in
another locale is read without complaint and read wrongly. Measured on 40-row
files built for this:

| Written | Stored | |
|---|---|---|
| `1.200,50` (de/es/it/nl) | `1.2005` | no error, 1,000x too small |
| `1 200,50` (fr) | `120050` | no error, 100x too large |
| `¥100` | — | the column becomes text and is never forecast (§4b) |

The first two are the worst thing that can happen to a file here: the column is
numeric, every row parses, nothing is printed, and the forecast is confidently
wrong. Nothing downstream can tell afterwards. The third is the opposite — quiet
on screen, loud in the database, where the column is simply missing from
`series`. Only `$`, `£` and `€` are stripped; any other currency symbol takes its
column out.

Re-download with the account set to a locale that writes `1200.50`, or convert
the separators before importing.

## 2a. How a campaign export is read

Real exports (Google Ads) carry **one row per campaign per day**: 15 campaigns
over 262 days arrives as 3,930 rows. That shape drives most of the design.

| What | What happens |
|---|---|
| Repeated days | Not an error. The column whose distinct values match the rows-per-day is the campaign column (override with `-by`). Only a **label** qualifies: a text column, or a numeric one that looks like an identifier (`Campaign ID`). A measured column is never chosen, however well its value count happens to fit — see below. |
| Each campaign | Forecast on its own series. |
| The account | The sum of every campaign per day, forecast as its own series. `(account)` is its name. |
| Uneven rows per day | **Refused.** A day missing a campaign would put a step in the totals that never happened. `-fill-absent` is the one way past it, for exports that list a campaign only on the days it ran — see §2a2. |
| Text columns | Stored, never forecast. |
| `Campaign ID` and similar | Numeric, but a label. Stored, never forecast — adding fifteen together gives 327,129,489,016. |
| Rates (CTR, conversion rate) | **Forecast like anything else.** Percentages parse as numbers ("4.20%" -> 4.20, kept as written) and the sign goes back on in the report. What a rate is *not* is addable, so the account figure is the **mean** across campaigns, not the sum. |
| Settings (budget, bid, target CPA, caps) | **Stored and aggregated, never forecast.** A budget is a dial you turn; forecasting it just replays the number you set — the real file produced seven days of 5877.00. Cost per acquisition is *not* a setting: it is cost divided by conversions, an outcome you measure. |
| Paused campaigns | **Stored in full, never forecast, never trained on.** See §2a1. Named both in the terminal and in the report — a campaign that simply vanishes from the page reads as an omission. Asking for one by `-entities` says why rather than "no campaign named". |
| Every original row | Kept verbatim in the `raw` table. |

The account and the campaigns are forecast **independently**, so their totals
will not match exactly. On the real file they agree to within 1–4%, which is a
useful sanity check rather than a guarantee.

**Two guards, because counting rows is not enough.** The first checks that every
day carries the same *number* of rows. The second, `sameCampaignsEveryDay`,
checks that it carries the same *set* of them, and runs once the group column is
known. Only the row count existed at first, and a day that listed one campaign
twice and another not at all passed it: measured on a two-campaign file, `Brand`
absorbed both rows (1119 against a true 120) and `Shopping` was stored as a real 0
it never reported — a fabricated step in exactly the series the guard exists to
protect, with the account total still correct so nothing looked wrong.

Both now refuse the file and name the day, the campaign and which way it differs.
A campaign that appears on one day and nowhere else cannot arise on its own: it
changes the file's distinct count and `findGroupColumn` refuses first.

**A measured column is never used to split campaigns.** The fallback to a numeric
column exists for `Campaign ID`, which is a real answer. It used to accept any
numeric column with the right number of distinct values, so a file whose campaign
names changed mid-period was silently grouped by **`Cost`**: prices became the
campaign names, and `Cost` left the forecast entirely. `findGroupColumn` runs after
classification (§4b), so it now asks the question the rest of the tool already
answers — a numeric column is a candidate only when `looksLikeIdentifier` says it
is a label. If the only column that fits is a quantity, the file is refused and the
message names it; `-by` on a metric is refused for the same reason rather than
obeyed.

**A campaign renamed or replaced part-way through the export cannot be imported
at all.** `findGroupColumn` counts a column's distinct values over the *whole
file* and requires that count to equal the rows per day, so a file with two rows
every day but three campaign names in it is refused:

    there are 2 rows per day but no column has exactly 2 distinct values, so they
    cannot be told apart. Name the column with -by NAME

Naming it does not help. `-by "Campaign"` on the same file answers

    -by "Campaign" has 3 distinct values but there are 2 rows per day, so it does
    not separate them

Both were measured. No flag imports such a file; the fix is in the export — split
it at the rename, or re-export a range over which the campaign set is constant.
The message's advice is honest for the commoner cause (a column the tool could
not pick between) and a dead end for this one.

## 2a1. Everything is stored; only switched-on campaigns are forecast

These are two separate decisions, and conflating them is the mistake to avoid.

**Storage takes everything.** Every row of the export goes into `raw`, and every
campaign — enabled, paused, removed, long dead — gets its full daily series in
`series`, alongside the `(account)` total, which is the sum of *all* of them
because that is what the account actually spent. Nothing is filtered on the way
into the database. A campaign that is paused today may be switched back on next
month, and its history has to already be there when it is.

**Modelling takes only what is switched on.** `runningEntities` (`ingest.go`)
reads the export's campaign-status column and keeps the campaigns it says are
enabled *as of the file's last day*. Everything else is put in `d.Paused`, which
is a subset of `d.Inactive` — stored, named in the output, and never passed to
TimesFM, Chronos-2 or the fine-tune. `d.Stopped` is the other subset and gets the
same treatment for the same reason, on exports that have no status column to read
(§2a2). `models/finetune.py`'s `running_groups` applies the
same rule, so the adapter is fitted to exactly the series it will be asked about.

**Why, concretely.** A paused campaign's next seven days are a decision, not a
forecast: it spends nothing until someone turns it back on. Asking a model anyway
returns noise hovering around zero, and on a real run `Patches DSA #2 - Zombie`
(last spend 2025-11-04) came back with quantiles 6.9% out of order, failed
`checkForecast`, and took the entire second report down with it. The old rule
only caught series that never moved *at all*, which scales with the window: on
the same file 9 of 15 campaigns were excluded at 180 days but only 2 at 1,099.
Status is absolute, so it does not drift with how much history you export.

**Finding the column.** By its values, never its name. A Google Ads export has
`Campaign status` (Enabled/Paused) *and* `Status` (`Eligible (Limited)`) *and*
`Status reasons`; matching the word "status" picks the wrong one. A text column
is believed only when every value in it is a word in `campaignStates`
(`ingest.go`) / `CAMPAIGN_STATES` (`models/finetune.py`) — **these two lists must stay
in step.** No such column, and the tool falls back to the never-moved rule alone.

**The exact-match rule stops protecting you once the account is small.** Requiring
a column's distinct values to equal the rows per day is what normally keeps
`findGroupColumn` off the status column — but a two-campaign export with one
enabled and one paused gives `Campaign status` exactly two distinct values too.
Measured on a 40-day, two-campaign file:

    2 rows per day, and several columns could separate them (Campaign status,
    Campaign). Choose one with -by NAME

Three campaigns in three different states do the same. Unlike a campaign set that
changes over time (§2a), this is always recoverable: `-by "Campaign"` is the
answer. Pass `-by` by habit on any account small enough for the two counts to meet.

**The fallback is silent, and the only tell is the wording of one line.** A single
value anywhere in the column that is not a word in `campaignStates` disqualifies
it for the entire file. Nothing announces that; the tool drops back to the
never-moved rule, and a paused campaign whose history is not flat gets forecast
after all. What distinguishes the two states is which exclusion line is printed:

| Line | Means |
|---|---|
| `switched off in the export, stored but not forecast:` | the status column was found and used |
| `no activity at all, stored but not forecast:` | it was not; only the never-moved rule applied |

Measured on two 3-campaign, 120-row files identical but for one cell (`Pending`
instead of `Enabled` on a single row), which flipped the output from the first
line to the second. To check a file before importing it — adjusting the field
number to wherever the status column sits:

    awk -F, 'NR>1{print $2}' "Campaign report.csv" | sort -u

Every word that comes back has to be in `campaignStates`. If one is a real
platform state, add it there and to `CAMPAIGN_STATES` in `models/finetune.py` in
the same change.

**The dropdowns follow automatically.** The report's campaign dropdown is built
from `sharedEntities(runs)` — the entities every model actually forecast — not
from a query over `series`. Paused campaigns are in the database but not in the
dropdown, which is the intended behaviour: you can only pick something there is a
forecast for.

**The account is forecast as its own series, so the report's total row does not
equal the campaign rows above it.** The single-model report's summary table draws
each campaign's next-horizon median total and then `(account)` as a `tr.total` with
a rule above it — which reads exactly like a column sum and is not one. `(account)`
is a separate entity with its own history, sent to the model as its own series.
Nothing reconciles the two, and nothing should: the model is free to be more
confident about a smooth aggregate than about its parts. Measured on
`examples/05-campaigns.csv` at horizon 7 with `chronos2`: campaign rows summed to
1922.06 for Cost against an `(account)` row of 1933.59. The excluded campaign is not
the cause — it is all-zero across every row.

Verified on the real 1,099-day, 15-campaign export: 6 campaigns plus `(account)`
forecast; 9 stored and named as switched off.

## 2a2. Ragged exports: `-fill-absent`

Not every platform lays its export out the same way. Some emit a row for every
campaign on every day and zero-fill the quiet ones; others list a campaign only
on the days it actually ran, so a day near the end has more rows than a day near
the start. **This is about the shape of the file, not the platform that produced
it** — nothing in the code knows or asks which ad network an export came from,
and nothing should be added that does.

A dense export needs nothing. A ragged one is refused by the rows-per-day rule
(§2a), because the tool cannot tell a campaign that was not running from a
campaign whose row went missing. `-fill-absent` on `forecast` and `import`
answers that question for it: **add a zero row for every day outside each
campaign's own run.** Measured on a real 116-day ragged export: 467 rows added
across 7 campaigns, giving an even 12 rows a day.

Three parts of the rule matter, and all three were arrived at by breaking it:

- **Only outside the run.** A day missing from between a campaign's first and
  last row is a hole in the download, not a campaign that was off, and it is
  **refused** with the campaign named and its run printed. Filling it would
  invent a zero on a day that did have spend. The flag repairs a shape; it does
  not paper over a bad export.
- **The filled rows never reach `raw`.** `raw` is the record of what the platform
  actually sent, and a zero it never sent does not belong in it. They appear in
  `series` only, which is the grid the models read. On that same export: 925 raw
  rows, 1,392 grid rows.
- **A campaign the export stops listing has stopped running.** Its rows ending
  before the file's last day is a fact about the export, not an inference from
  its values, and it is the shape-based equivalent of a status column saying
  paused. Such campaigns go in `d.Stopped` — another subset of `d.Inactive` —
  stored in full, named in the output, never forecast (§2a1).

That last part is not a refinement, it is the reason the feature works at all.
The first run with the fill and without it failed exactly as §2a1 predicts:
`Testing 5` ran for 22 of 116 days, so 94 of its days were filled zeros, and
`chronos2` came back with quantiles **67.3%** out of order and took the run down.

A campaign that started late is the other case and is **not** stopped: it is
running on the last day, so it is forecast normally.

**The flag is safe to leave on.** On a dense export it changes nothing — no rows
added, no campaign marked stopped, the same numbers. Measured on a real 1,099-day
dense export: every line of output identical with the flag and without it, down
to the 16,485 raw rows and the same 13 excluded campaigns. `TestFillAbsentChangesNothingOnADenseExport`
holds that.

**The label column.** The fill needs to know which column names the thing each
row is about, and finds it the same way §2a does — by shape, preferring a text
column, accepting one that `looksLikeIdentifier` accepts. `-by` overrides it, and
the fill honours it, so the zeros cannot be grouped by one column while the
forecast splits campaigns by another. If no column identifies a row within its
day, the error names `-by` and lists the columns.

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
main.go          commands: setup, models, import, report, forecast, runs, accuracy, version
import.go        the recurring job: data/ -> forecasts -> two reports -> imported/
compare.go       the multi-model comparison report
compare_template.go  its page, with the campaign and metric dropdowns
rerender.go      report: redraws those pages from stored runs, reading only
ingest.go        CSV -> Data (dates, numeric columns, skipped text columns)
worker.go        the model protocol + output validation   <- read this first
db.go            SQLite: raw, series, runs, forecasts
report.go        builds the HTML report, draws the SVG charts
template.go      the report page, with htmx embedded via go:embed
format.go        number formatting for terminal and report

models/
  timesfm3_worker.py   load the model, answer requests
  chronos2_worker.py   same, plus covariate support
  chronos2ft_worker.py same again, with the LoRA adapter applied on top
  finetune.py          trains that adapter and registers it
  weights_check.py     recomputes the weights sha256 before use
  fetch.py             downloads weights at pinned revisions
  requirements.txt     pinned, verified working together
  .venv/  cache/       created by install.sh, not in the repo
  finetuned/  finetuned.json   written by finetune.py, not in the repo

examples/
  01-simple.csv  02-marketing.csv  03-platform-export.csv  04-with-budget.csv
  05-campaigns.csv     several campaigns per day: the per-campaign + account case
  walkthrough.sh       the single-file commands, run for real (not import/report/accuracy)
  ground-truth.py      proves stored numbers are the library's, unaltered
testdata/        fixtures, including deliberately broken workers
dist/            prebuilt binaries for people without Go: built by build-dist.sh
                 when you are about to share the folder, not in the repo
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

Four rules on this seam:

1. **stdout belongs to the protocol.** Workers take the real stdout and redirect
   `sys.stdout` to stderr, because a progress bar printed by a library would
   otherwise corrupt the JSON stream. This has already happened once.
2. **A worker file must not be named after a Python package.** A script's own
   directory goes first on `sys.path`, so naming a worker after the `timesfm3`
   package shadowed the real one and broke the import. Hence the `_worker.py`
   suffix on every worker.
3. **Capability mismatch fails loudly.** Sending covariates to a model that
   declares `covariates: false` is an error, never a silent drop.
4. **The handshake's `quantiles` must be ascending, and the reply must use that
   same order.** Nothing verifies it. `checkForecast` sorts every day's values
   ascending as its repair for crossing, and `saveRun` then labels position *j*
   with `quantiles[j]` — so a worker that declares `[0.5,0.1,0.9]`, or returns
   its columns in a different order from the one it declared, has its median
   stored as the 10th percentile with no error anywhere. Chronos-2 will not save
   you: it honours `quantile_levels` in whatever order it is asked, including
   `[0.9,0.1,0.5]` (measured). Whether this is caught depends only on how wide
   the interval is — a wide one trips `absurdCrossing` and is refused with a
   message about the *forecast* being out of order, which sends you looking in
   the wrong place; a narrow one is silently sorted and reported as an ordinary
   ~0.1% crossing. Measured: a worker declaring `[0.1,0.2,0.9]` and answering
   `[101,100,900]` was accepted and stored as `q0.10=100 q0.20=101`. §5's
   "quantiles ascending" is about the model's output values; this is about the
   declared levels, which is a different fact. Declare them ascending, return
   them ascending.

**The `quantiles` line is not advisory, and the three models treat it
differently.** Go always sends back exactly what the handshake declared, so this
never shows up in normal use — but it decides what is possible if you ever want a
different grid. `models/timesfm3_worker.py` compares the request against its own list and
refuses anything else outright, because TimesFM 3.0 returns a fixed nine.
`models/chronos2_worker.py` and `models/chronos2ft_worker.py` pass the list straight through as
Chronos-2's `quantile_levels` and return whatever you ask for — including levels
the handshake never mentioned, duplicates, and levels out of order (all measured).
A change to the quantile grid is therefore a two-model change that will appear to
work on Chronos and fail on TimesFM.

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
stale rows. **`series` is replaced the same way**, per `series_id`, so the two
tables always describe the same file.

That replacement is deliberate and was not always there. `saveData` used to be an
upsert with no delete, which only touched the cells the new file covered — so a
re-import that *narrowed* the export, or an import for an entirely different
account under the same name, left everything it did not mention behind. `raw` was
then exactly the newest file while `series` was the union of every import ever
done, and since `forecast_accuracy` joins forecasts to `series` and never to
`raw`, those disowned rows kept being scored as though they were actuals.
Measured before the fix: seven days scored against numbers present in no raw row.

The consequence to understand: **a re-import discards the previous dataset of the
same name.** Runs and forecasts survive that replacement — nothing references
`series` — but a forecast made against data you have since replaced simply stops
having an actual to score against. (That is `saveData`/`saveRaw` on their own,
which is all `forecast` does. `import` goes further and deletes the runs as well
— see the wipe below.) That is the honest outcome, and it is what makes importing a
different account under an old name safe. Use a distinct `-series` name if you
want two accounts side by side.

To confirm the two tables agree, which they now do by construction:

    SELECT COUNT(*) FROM series s
    WHERE NOT EXISTS (SELECT 1 FROM raw r
                      WHERE r.source = s.series_id AND r.day = s.day);

**Nothing deduplicates a run.** `input_sha256` is written and never read, so the
same `forecast` command run twice stores two runs and `accuracy` counts both:
measured, `days` went 7 → 14 → 21 over three identical invocations, with every
percentage unchanged. See the `days` note below before comparing models.

**Every campaign is in `raw` and `series`, including paused ones** (§2a1). The
filter is on what gets *modelled*, never on what gets stored.

**Every forecast is kept, not just the one that was drawn.** Each model's run is
its own row in `runs`, carrying `as_of` (the last day of real data it was given)
and `model_info`; every predicted day, entity, metric and quantile is a row in
`forecasts`. So an import on 2026-09-23 stores what TimesFM and Chronos-2 said
over each window, what their average said, and what the fine-tune said about
2026-09-24 through 2026-09-30, at full precision, before any of it was known.
When the next export arrives it lands in `series`, and `forecast_accuracy` joins
the two on `(series_id, entity, metric, day)` so each prediction can be scored
against what actually happened — per day, per model, per days-ahead. That join is
the whole reason the forecast table exists; nothing else reads it after the report
is written.

That scoring only happens if the actuals arrive by a route that keeps the
forecasts: **a second `import` wipes them before it stores anything**, so the
prediction and the outcome never meet. See the wipe below, and use `forecast` if
you want a history worth scoring.

Two indexes are declared beyond the primary keys: `raw_day` on `(source, day)`,
and `forecasts_median`, a partial index on
`(entity, run_id, metric, day) WHERE quantile = 0.5`. The second one exists because
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

**WAL and read-only media are two different things.** A database file that is
read-only in a *writable directory* opens and queries fine — that is what
`TestReadOnlyDatabaseCanBeQueried` pins, and why `user_version` is stamped only
when the schema is actually written. A database on genuinely read-only media — a
read-only directory, a mounted snapshot, a share you were handed — cannot be
opened at all, because SQLite must create the `-shm` file before it can read a WAL
database. The failure is reported against the *first* statement, so it reads like
a corrupt file rather than a permissions problem:

    error: reading schema version of /path/pm.db: attempt to write a readonly database (1544)

Repair: put the file back in `journal_mode=DELETE` **while it is still on writable
storage**, then move it. A DELETE-journal database needs no sidecar and opens
read-only with no change to this program.

    sqlite3 pm.db 'PRAGMA journal_mode=DELETE'

`openDB` switches it back to WAL the first time it is opened somewhere writable,
so this is a property of the copy, not of the database. The existing test cannot
catch this: it chmods the file only, and `t.Skipf`s with the words "WAL cannot be
read-only" — which is the whole fact, living in a skip message.

**`CREATE TABLE IF NOT EXISTS` is not a migration.** On a file that already has
the table it ignores the new definition completely, without error. That is not
theoretical: this repo's own `pm.db` predated per-entity storage, every statement
appeared to succeed against it, and `accuracy` then failed with a bare
`no such column: entity`. `checkSchema` now gates every open — it reads
`user_version`, adopts a current-shaped file that simply predates stamping, and
refuses one it cannot read with an explanation and a way out. `user_version` is
stamped only when the schema is actually written, which is what keeps opening an
up-to-date file a pure read. Bump `schemaVersion` and teach `staleTable` (via
`requiredColumns`) the new columns whenever a table changes shape.

**The view and the indexes are handled separately, because `schemaVersion` cannot
speak for them.** They used to sit inside `schema` behind `CREATE ... IF NOT
EXISTS`, which ignores a new definition just as surely — and unlike a table there
is nothing for `staleTable` to inspect, since a view has no columns of its own to
miss. A file carrying an old definition was therefore adopted in silence and kept
it. Reproduced: a deliberately stale `forecast_accuracy` made `accuracy` **exit
0** printing `no forecast day has an actual yet` on a database holding 105
scorable rows.

They are now `derivedObjects` — the view and both indexes, everything that holds
no data of its own — compared against the file on **every** open by
`refreshDerived` and rebuilt when the text differs. SQLite stores each `CREATE`
verbatim, so the comparison is exact. It is a pure read when nothing has changed,
which is what keeps opening a current database free of writes and still possible
on a read-only file, and two processes racing to create the same object is success
for both.

**So changing the view or an index needs no `schemaVersion` bump** — edit the DDL
in `derivedObjects` and every existing database picks it up on next open. Adding a
new derived object means adding it to that slice, not to `schema`.

**`import` empties the database first.** An import is someone bringing in an
account, and the numbers already stored belong to whatever was there before;
merging two accounts' histories under one roof produces totals that describe
nothing. `clearDatabase` deletes `forecasts`, `runs`, `series` and `raw` in that
order — `forecasts` references `runs` and `foreign_keys` is on — and the schema
and the derived objects survive.

Two details that are load-bearing:

- **The wipe happens after every model has answered**, not before. `forecastModel`
  returns a forecast and writes nothing; `storeRun` writes one that has already
  been made. `importOne` therefore computes the whole job — every window, both
  models, the average — and only then empties the database and stores it, in one
  pass.

  This was not the first shape. The wipe originally sat after the CSV parsed but
  before any model ran, which protected against a malformed file and nothing
  else. Measured with a deliberately broken worker: the old forecasts were gone,
  one orphaned model run had replaced them, and no report was written — strictly
  worse than not running the command. Now the same failure leaves the database
  byte-for-byte unchanged, verified at 5 runs / 6,615 forecasts / 7,360 series
  rows before and after.
- **Only the first file of a run wipes.** Dropping three exports in `data/` at
  once is one import of one account, not three accounts in sequence.

**This discards the forecast record, and that is the cost.** `runs` and
`forecasts` are the only evidence of what was predicted *before* the outcome was
known, and `accuracy` exists to score them once the actuals arrive. After a wipe
there is nothing left to score, so **`accuracy` cannot measure anything across
imports** — the forecast made this week is deleted by next week's import, which
is the very import that brings the actuals to judge it against.

`forecast` does **not** wipe. It replaces `series` and `raw` per dataset and
appends runs, so a workflow built on that command still accumulates a history
worth scoring. That is the route to a working `accuracy`, and it is what the
walk-forward backtest behind the window choice (§2c) used.

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

**Read the `days` column first — it is a sample size, not a date range.** It is
`COUNT(*)` over scored forecast rows, and nothing deduplicates by run. Two runs of
the same model at the same `as_of` contribute their rows twice: measured, a second
identical `forecast` run took one model's `days` from 7 to 14 while the other
stayed at 7 and every percentage was unchanged. The two were then being compared
on unequal samples with nothing on screen saying so. `runs` is where the
duplication is visible; `accuracy` will never mention it.

**`in range` means nothing until `days` is large.** It is `AVG(inside_range)*100`
over those same rows, so one backtest of a 7-day horizon gives seven observations,
and `-by-day` divides them into seven rows of **one**. Every `-by-day` cell then
prints 0% or 100%, which reads like a finding and is a single coin flip. The
"should sit near 80%" guidance needs several backtests behind it; below roughly
ten observations per row, read `avg error` and ignore `in range`.

**The filters are matched case-insensitively and then queried case-sensitively.**
`known()` accepts a name by `strings.EqualFold`, but the value is bound into
`WHERE entity=?`, which SQLite compares byte for byte. So `-entity "brand search"`
passes the guard and then matches nothing, producing the empty table §9 lists as a
*fixed* defect. **`0 ... still waiting` is the tell**: a real dataset almost always
has days that have not happened yet, so a zero there means the filter matched
nothing, not that everything has been scored. `-metric` and `-entity` are whole
names, not prefixes — `-entity "Brand"` does not select `Brand Search`.

**A typo'd `-series` reports an empty database.** `-series` is only a scope for the
name lookup and is never validated on its own, so a misspelling on a database full
of forecasts answers `no forecasts stored yet -- run predictmarketing forecast
first`. That is a typo, not an empty file; `runs` lists the names that exist.

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
| Not numeric | — | stored in `raw`, never forecast |
| Identifier | last **word** is `id`, `ids` or `code` | stored, never forecast |
| Setting | contains `budget`, `bid`, `target`, `limit`, `cap` | stored and aggregated, never forecast |
| Rate | contains `ctr`, `rate`, `%`, `ratio`, `share`, `avg.`, `avg ` or `average` | forecast; **averaged** across campaigns, not summed |
| Anything else numeric | — | forecast, summed across campaigns |

**"Not numeric" is decided by counting, and the count has a threshold.** A column
is numeric only if *every* cell parses. What happens when some do not depends on
how many:

| Cells that fail | What happens |
|---|---|
| fewer than one in ten | the import is **refused**, naming the first: `line 2: column 3 (Clicks): not a number: "--"` |
| one in ten or more | the column is silently set aside as text — stored in `raw`, never forecast |

The denominator is *rows*, not days, so on a real export (15 campaigns over 1,099
days is 16,485 rows) the second case is out of reach and a handful of `--` or
empty cells refuses the whole file. It names **one line at a time**: measured on a
40-row file with three bad cells, fixing line 2 produced the same error on line 3.
Fix them all at once, or drop the column from the export.

The silent case is the one to know about, because the only thing that reports it
is `forecast`'s `stored but not numbers:` line — and **`import` does not print
that line at all**. `importOne` prints `forecasting:`, the entity list and the
exclusion lines, and nothing about text, identifier or setting columns. On the
recurring job a demoted column just stops appearing in `forecasting:`. Read that
list every time.

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
trained on in `models/finetuned.json`. The worker checksums the adapter before use. It does
**not** checksum the base weights: `models/chronos2ft_worker.py` is the one worker
that does not call `load_verified`, because the adapter is loaded through the
adapter's own config rather than through `models/weights.json`. It reads `models/weights.json`'s
`chronos2` entry only for the *path*, and the `base_weights_sha256` in its
handshake is copied out of `models/finetuned.json`, where the trainer wrote it — a
record of what was trained on, not a check of what is loading now. So the
provenance guarantee this project makes for `chronos2` and `timesfm3` is half as
strong for `chronos2ft`: tamper with the adapter and it refuses; tamper with the
base weights under it and it forecasts happily. Run `chronos2` once if you want
those bytes verified — it is the same file. §9 lists "weights sha recorded but
never verified" as a fixed defect; it is still a claim for half of this model.

Two more things its handshake does differently, since `runs.model_info` stores it
verbatim and the reports read it back:

- `weights_sha256` carries the **adapter's** hash, not a model's. `models` prints
  it under "weights sha256" and the comparison report's provenance row prints it
  under "Weights", with no hint that it means something else here. The base hash
  sits beside it as `base_weights_sha256`.
- Starting the worker can **write to `models/`**: `load_registered` rewrites the
  adapter's `base_model_name_or_path` in place whenever the recorded base path no
  longer matches `models/weights.json`. That is what lets the project be
  moved, but it means `models` and `report` are not read-only once `chronos2ft`
  exists.

**The trainer never stops, shortens, or passes judgement.** `--steps` is the
whole instruction and every step runs, whatever the data looks like and however
long it takes. Its only two exits are a column that is not in the file (before
any training happens) and an adapter that was not written (after all of it);
neither looks at the size or shape of the data. `TestTrainingIsNeverGatedOnTheData`
holds the line, alongside `TestTrainingIsNeverTimeLimited`.

This has been got wrong twice, in both directions. A `--budget` wall clock cut a
real run to 1,210 of 2,000 steps (§2c). And the trainer printed a `NOTE:` when a
file had fewer than 50 series, saying fine-tuning had measured *worse* than the
stock model — true (below), but it could not act on it, the reader could not act
on it either, and mid-run it read as a failure in an otherwise clean import. It
is gone from the output. Whether the adapter actually helps is a question for
`accuracy`, measured on days the model never saw, not a guess made beforehand.
Facts about how well it does belong in these docs, where someone is reading
deliberately — never printed at a user who is waiting for a run to finish.

**Its two defaults are Google-Ads-shaped, and `trainFinetune` overrides both.**
`models/finetune.py` defaults to `--metrics Cost,Impr.,Clicks --group Campaign`,
which only `examples/05-campaigns.csv` carries. `import` passes the metric and
group columns **the file it just read actually has** (`data.Names`,
`data.GroupBy`), so the trainer is fitted to the same columns and the same
campaigns the forecaster ran.

It did not always. When it passed nothing, the defaults failed in opposite ways:

| The file | What happened |
|---|---|
| has the metrics under other names (`Impressions`, `Spend`) | the trainer exited before training: `columns not in <file>: ['Impr.']`, listing what the file does have. A real Meta export failed exactly this way, and that is how the bug was found |
| has no `Campaign` column, or calls it `Campaign name` | **it trained, on one series.** Every row collapsed into `(account)`, `running_groups` returned `None`, and the only sign was the leading `training on 1 series x N days x 3 metrics` |

The second was the dangerous one: it succeeded, it registered, and
`train_series: 1` in `models/finetuned.json` was the only record. That field is
still worth reading before believing a fine-tune covered the campaigns — running
the trainer **by hand** still gets the old defaults, so pass the flags yourself.
`import` now prints them in the command it suggests when a fine-tune fails.

**`load_series` skips the same campaigns the forecaster does**, and has to. It
already dropped the switched-off ones (`running_groups`) and the ones that never
moved; it also drops a campaign whose **rows stop before the file's last day**,
which is how a ragged export says a campaign stopped (§2a2). Without that the two
sides disagree: on the Meta export the forecaster ran 11 campaigns while the
trainer would have spent 2,000 steps fitting 13, two of them dead tails of zeros.
Measured after the fix: `not training on 2 switched-off, stopped or never-active
campaign(s): Testing 5, Testing 8 CBO Winners` / `training on 11 series x 116
days x 3 metrics`.


**The trainer refuses what the forecaster refuses.** `num()` in `models/finetune.py`
mirrors `parseCell`: currency symbols, thousands separators,
percent signs, spaces and parenthesised negatives all read the same way, and NaN
and Infinity are rejected rather than accepted. They diverged at first, in both
directions — `--`, `£10`, `(1,234.00)` and `1 234` killed the trainer with a bare traceback on files the forecaster reads fine, while `NaN` sailed through into the
training matrix on the one file the forecaster refuses outright. Keep them in
step: a cell either side rejects is a cell neither should model.

**`--horizon` and `--group` are accepted and recorded nowhere.** The registry keeps
`steps`, `batch_size`, `context_length`, `learning_rate`, `train_series`,
`train_seconds`, `trained_through`, `adapter_sha256` and the base model's identity —
but not the horizon the adapter was fitted for, nor the column it was split by.
`train_seconds` brackets `fit()` only, not loading or saving. The file is rewritten
whole each run, so a key the current trainer no longer writes (`hit_time_budget`)
means the registry predates it.

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
same peak memory at batch 8. LoRA was chosen for the roughly 90x smaller checkpoint (456 against 4.9, as
the table prints them), which makes keeping a history of them practical.

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

**The guard compares strings, and only one side is normalised.** `ingest.go`
rewrites every date to `2006-01-02` before storing it, so `forecasts.day` is always
ISO. The trainer does not: it takes `days = sorted({r[0] for r in data})` straight
off column 0 and records `days[-1]` verbatim. That value travels unchanged into
`models/finetuned.json`, into the handshake, into `runs.model_info`, and into
`f.day <= json_extract(..., '$.trained_through')`.

On any export Go accepts but Python cannot sort — `28/01/2026`, `2 Jan 2026` — two
things break at once and neither says anything. The training matrix is built on a
**lexicographic** day index, so the series reaches the model out of order; and
`trained_through` is the lexicographically last date, not the latest, so the SQL
comparison is ISO-against-not-ISO. Measured on a day-first file the tool imports
happily: the trainer sorted `['02/02/2026','03/02/2026','28/01/2026','29/01/2026']`
and built the Cost row as `[12, 13, 10, 11]` where the file's order was
`10, 11, 12, 13`. The comparison then goes **both ways** depending on the dates:

```sql
SELECT '2026-01-29' <= '29/01/2026';   -- 1: every row marked trained_on,
                                       --    chronos2ft vanishes from `accuracy`
SELECT '2026-06-03' <= '03/06/2026';   -- 0: guard off, memorised days scored
```

The first outcome is indistinguishable from the documented-correct "chronos2ft is
absent because every forecast is inside its training window". **Before trusting an
absence or a score, check that `trained_through` in `models/finetuned.json` is
`YYYY-MM-DD`.** If it is not, the export was not ISO-dated: convert the date column
and retrain. Google Ads exports ISO, so this does not bite the normal path.

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
- **Every transaction in `db.go` writes first, and that is load-bearing.** A
  transaction that reads before it writes has to upgrade its snapshot, and under
  contention SQLite fails that upgrade **immediately** — `busy_timeout` does not
  apply to it. Measured against a concurrent writer: 1.03s elapsed with
  `busy_timeout=10000`, not 10s. `BEGIN IMMEDIATE` fixes it. Latent today only
  because no transaction here reads first; keep it that way, or take the
  immediate lock.
- **`inside_range` only works for a model that returns exactly 0.1 and 0.9.** The
  view's `low`/`high` are self-joins pinned to those literals. A model with any
  other band yields NULL, which `accuracy` prints as **`in range 0%`** —
  indistinguishable from a model whose band never contained the actual.
  Demonstrated with a synthetic q05/q95 run: same medians, same error, `0%`
  against `100%`. Widening the band means widening the view.
- **Store every campaign; model only the switched-on ones.** Nothing is filtered
  on the way into the database — paused campaigns keep their full history. The
  status rule applies to the model calls and the fine-tune, and to nothing else.
  See §2a1. `campaignStates` in `ingest.go` and `CAMPAIGN_STATES` in
  `models/finetune.py` must agree: training on a campaign the forecaster then
  refuses to run fits the adapter to series nobody will ever see.
- **Never derive one metric from another.** Every metric is forecast by the model
  itself. No ratios, no percentages. `multivariate_test.go` proves this with three
  unrelated shapes and a perturbation test. Keep those tests.
- **Validate every forecast before storing it**: right shape, no NaN or infinity,
  quantiles ascending. A failing forecast fails the run; nothing is written.
  Negative values are legitimate and are *not* rejected.
- **Quantile crossing is repaired, not rejected.** Both models predict each
  quantile independently, so mild crossing (~0.2%) is expected. It is fixed by
  sorting — monotonic rearrangement, a standard and strictly improving correction —
  and the size of the largest correction is reported **by `forecast`**. `import`
  discards it: only `main.go` reads `LastCrossing`, so on the recurring job a
  repair happens silently. If you want to know how much rearranging a model
  needed, run `forecast` for that model once.

  The check has a floor. `negligible := scale < 1e-6` is **absolute**, not
  relative to the series' own size, and below it the whole ordering test —
  including the 5% refusal — is skipped. Measured: a fully inverted forecast at
  `1e-7` was accepted with `worst=0`, while the same shape at `1e-5` was refused
  at 25%. Marketing units never go that small, so this is a documented mechanism
  rather than a live hazard; it matters if this tool is ever pointed at a series
  in different units. Crossing beyond 5% is refused.
- **Day/month order is decided once per file**, never per row. A file where nothing
  settles it is refused, because guessing shifts every date by up to eleven months.
- **Resolve paths against the binary**, not the working directory (`installDir`).
- **Mark deliberate shortcuts** with a `ponytail:` comment naming the limit and the
  upgrade path.

## 6. Settled decisions — do not reopen

| | |
|---|---|
| Language | Go for everything except the Python in `models/` — the three workers, the trainer and the two weights helpers, 662 lines in all. Not "100% Go" — earlier drafts of these docs said so and were wrong. |
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
  **That timeout covers the reply only.** It lives in `readLine`; `startWorker`
  waits for the handshake with a bare blocking `Scan()` and no deadline, so a
  worker that stalls *while loading* rather than exiting hangs the command
  forever, with nothing printed after the model library's own progress bar.
  Measured: a fixture that sleeps without printing a handshake was still blocked
  after 20s. A worker that dies before the handshake is handled and tested
  (`testdata/badworkers/nohandshake_worker.py`); one that never returns is neither, since
  `testdata/badworkers/hang_worker.py` sends its handshake first. If a model command sits silent for minutes, suspect
  that shape: Ctrl-C and check the worker's startup path, not the forecast.
- Python side installed: **~820 MB** (torch is ~570 MB of it). Weights: **1.7 GB**.
- A clean `./install.sh` measured **182 seconds** on a fast connection.
- `go test .` **33s**; `go test -race -count=2 .` **71s**; `sh examples/walkthrough.sh`
  **28s**; fuzzing runs ~1,100 exec/s. On `examples/05-campaigns.csv` (150 days,
  so `full` and `90d` — four model runs), `import -no-finetune` takes **14s**
  against `report`'s **0.02s** — which is why `report` exists for drawing
  changes. A file long enough for all three windows pays for six runs, not four.
- Cross-compiles cleanly to six targets; `build-dist.sh` ships four. No Windows
  build is shipped, because `install.sh` is POSIX sh.
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
./predictmarketing forecast examples/02-marketing.csv -model chronos2 -db /tmp/gt.db
./predictmarketing forecast examples/02-marketing.csv -model timesfm3 -db /tmp/gt.db
models/.venv/bin/python examples/ground-truth.py /tmp/gt.db examples/02-marketing.csv
```

The script reads the runs back out of that database, so both forecasts have to be
made first; on an empty one it stops at `no such table: runs`.

Every line must say `EXACT`. The stored numbers must equal the library's output
exactly, sorted only where the model's own quantiles crossed.

To see the single-file commands run for real: `./examples/walkthrough.sh`. It
exercises `forecast`, `models` and `runs` only — every forecast in it prints
`for 1: (account)`, so it never forecasts a campaign and never touches
`examples/05-campaigns.csv`. It does not run `import`, `report` or `accuracy`, which
are the recurring job. Use it to see the protocol work, not as proof the workflow does.

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
| Falling back to any numeric column as the campaign column | a file whose campaign names changed mid-period was grouped by `Cost` — prices became campaign names and the metric left the forecast, silently |
| The console summary indexing the account unconditionally | every `-entities` run that excluded the account panicked with "index out of range [0] with length 0"; the report template had been fixed for this, the summary beside it had not |

| Counting rows per day but never the campaigns in them | a day listing one campaign twice and another not at all passed: the duplicate stored as 1119 against a true 120, the absent one as a fabricated 0, account total still correct |
| `CREATE VIEW / INDEX IF NOT EXISTS` treated as a migration | a stale `forecast_accuracy` made `accuracy` exit 0 reporting "no forecast day has an actual yet" on a database holding 105 scorable rows |
| `saveData` upserting where `saveRaw` replaces | a narrowing re-import left `series` holding rows the current export disowned, and `forecast_accuracy` scored against them |
| The trainer parsing cells its own way | `NaN` reached the training matrix on the one file the forecaster refuses, while `--`, `£10` and `(1,234.00)` killed the trainer on files it accepts |

None of these were in the models. All were in the surrounding code.

### What the suite does not cover

The table above is what the tests *do* guarantee. This is the other half, and it is
the half a future agent needs, because a green `go test` here is not the same thing
as a working tool. Measured with `go tool cover -func`: **60.2% of statements**.

**Whole commands are never executed by a test.** Coverage is 0.0% for every `cmd*`
function — `cmdForecast`, `cmdModels`, `cmdRuns`, `cmdAccuracy`, `cmdSetup`,
`cmdImport` — and for `main` itself. The units underneath them are well covered; the
flag parsing, the defaulting, the console output and the ordering of steps are not.
`TestHelpListsEveryFlag` pins that a flag is *listed*, never that it does anything.

**`rerender.go` — the entire `report` command — is 0.0% covered**, all seven
functions. `TestReportCommandIsDocumented` checks that five documents *mention* it
and that the help lists it. Nothing runs it. The `%`-sign limit those documents all
state is asserted as prose and verified nowhere. An exact-equality test is cheap and
available: `import -no-finetune` then `report` on `examples/05-campaigns.csv`
produce byte-identical pages — measured, `diff` is empty. That file has no rate
column, so it does not exercise the `%` limit; a fixture that does would.

**`import.go`'s orchestration is 0.0% covered** — `importOne`, `forecastModel`,
`trainFinetune`, `defaultPath` — while the units around it are covered.

**`TestDefaultsAnchorToTheInstallNotTheShell` never runs.** It skips under `go test`,
always: the test binary lives in the build cache, so `installDir()` finds no
`models/` beside it and the test's own guard skips. The rule it protects — that
`data/` and `pm.db` anchor to the installation — is therefore enforced by nothing
automatic and has to be checked by hand, as the `verify` skill says.

**`TestBinaryWorksFromAnotherDirectory` tests whatever binary is on disk.** `go test`
does not build `./predictmarketing`, so it either skips or runs a build from some
earlier commit. Measured: it passed against a binary reporting a different commit
from HEAD. Build first, or it is theatre.

**Thirteen tests vanish silently without `./install.sh`.** Measured on a fresh
clone with no `models/.venv` — exactly what `share.sh` hands someone — the suite
exits 0 and prints `ok` in about a second: 158 pass and 15 skip, against 172
passing and 1 skipping here. Two of those 15 skip anyway
(`TestDefaultsAnchorToTheInstallNotTheShell`, and `TestBinaryWorksFromAnotherDirectory`
because a clone has no binary). What actually goes missing is every protocol
test, both weights tests and the perturbation test: the checks that prove the
models are real. Worse, `TestEachMetricGetsItsOwnForecast` is reported as
**PASS** while both its subtests skip, so the count understates it. A green run
on a fresh clone means less than a green run here.
