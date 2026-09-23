# Predict Marketing

Forecast a series of daily numbers with **TimesFM 3.0** (Google) or **Chronos-2**
(Amazon), store the result, and get a chart you can open in a browser.

The code is Go. The models run unmodified in Python behind a small adapter.
Switching between them is one word on the command line.

```bash
./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 7
```

```
chronos2: 180 days of "example" -> 7 days ahead
  forecasting: spend
  for 1: (account)
  stored, not forecast (you set these, you do not predict them): budget

  (account) -- spend
  day                     low         median           high
  2026-08-28           837.18         866.75         893.20
  2026-08-29           815.05         844.17         870.13
  ...

saved run 121851a1b3b6 to pm.db
report: testdata/example_forecast_chronos2.html
```

Open that HTML file and you get the chart, the numbers, and a record of exactly
which model and which weights produced them.

---

## Installing

### Getting it

```bash
git clone https://github.com/eliquid/predict-marketing-forecasting.git
cd predict-marketing-forecasting
./install.sh
```

If someone sent you a folder instead of a link, skip the clone and run
`./install.sh` from inside it.

**`go install` will not work, and that is not a bug.** The models are Python and
their weights are 1.7 GB, so the program needs the `models/` folder sitting next
to it. A lone binary in `~/go/bin` has nothing to run and says so. Clone the
repository instead.

### Versions

Tested on these. **Nothing here is pinned to them** — the installer does not
compare version numbers against a hardcoded list, and neither newer nor slightly
older versions are refused.

| | Tested with | Required |
|---|---|---|
| Go | 1.26.5 | 1.24 or newer (`go.mod`) — needed only if you build from source |
| Python | 3.11.15 | 3.10 or newer; `install.sh` prefers 3.11 and accepts what you have |
| SQLite | 3.50.4 | **none** — it is compiled into the program |

Everything above, and every measurement quoted in this README, was run on:

> **MacBook Pro (14-inch, 2021)** — Apple M1 Pro, 10 cores (8 performance,
> 2 efficiency), 32 GB memory, macOS 26.1, arm64.

That is the only machine it has been tested on. It is pure Go plus Python, with
no platform-specific code, so Linux and Intel Macs should be fine — but "should"
is doing real work in that sentence, and nobody has checked. Windows builds and
runs the program, though `install.sh` is a shell script (see **Building**).

Timings scale with the machine. A forecast takes three to four seconds here
(3.1s and 3.9s on two consecutive runs of `testdata/example.csv`), almost all of
it loading the model rather than predicting — so a slower disk shows up more
than a slower CPU, and the horizon barely matters.

**You do not need SQLite installed.** The database is `modernc.org/sqlite`, a
pure-Go implementation built into the binary, so there is no system library to
match, no CGo, and nothing to go out of step. That is also why the program
cross-compiles to Linux and Windows without a toolchain.

Other versions should work and are simply untested. If one genuinely cannot run
the code, you will get a real error from Go or pip saying what is wrong, rather
than a version check refusing to try.

### What install.sh does

That is the whole thing. It sets up Python, builds the program, downloads both
models, and finishes by running a real forecast to prove it works.

- **Takes a while the first time.** About 2.5 GB comes down: ~800 MB of Python
  libraries and ~1.7 GB of model weights. Needs roughly 3 GB free. A clean install
  measured 3 minutes here on a fast connection; budget longer on a slower one.
- **Safe to run again.** Everything already done is skipped. If a download fails
  halfway, run it a second time.
- **The only thing it may install for you is `uv`** (the Python installer), and it
  asks first and shows you where it comes from.
- **Go is needed if you cloned this repository.** Prebuilt binaries are not kept
  in version control, so `install.sh` builds from source. Install Go from
  <https://go.dev/dl/> first; the installer says so and stops if it is missing.
  A folder produced by `share.sh` carries prebuilt binaries in `dist/`, and those
  are used automatically when Go is absent.

If you would rather do it by hand:

```bash
uv venv models/.venv --python 3.11
VIRTUAL_ENV=$PWD/models/.venv uv pip install -r models/requirements.txt
go build -o predictmarketing .
./predictmarketing setup
```

### Sharing this folder with someone else

```bash
./share.sh ~/Dropbox/predict-marketing
```

That makes a ~31 MB copy — the code, plus prebuilt programs in `dist/` for people
without Go. It leaves out the Python environment, the model weights and your own
databases, which is the 2.5 GB their `./install.sh` downloads fresh anyway.

Hand them the folder. They open a terminal in it and run **one command**:

```bash
./install.sh
```

Run `./build-dist.sh` first if you have changed the code and want the prebuilt
programs refreshed.

**Windows:** the program builds and runs, but `install.sh` is a shell script, so
use the by-hand steps above (they work in PowerShell with small changes) or run
it under WSL.

### Your first forecast

The installer finishes by telling you to run this, and it is the right next step
— it forecasts a file that ships with the project, so it works before you have
any data of your own:

```bash
./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 7
```

It prints the next seven days and writes `testdata/example_forecast_chronos2.html`.
Open that file in a browser: chart, numbers, and a record of exactly which model
and which weights produced them.

Then try the one with campaigns in it, which is what the tool is actually for:

```bash
./predictmarketing forecast examples/05-campaigns.csv -horizon 7
```

That forecasts each campaign **and** the account total, and tells you which
columns it forecast, which it only stored, and which campaigns it left out.

When you are ready for your own numbers, **The recurring job** below is the
command you will actually use day to day: drop the CSV in `data/`, run
`./predictmarketing import`, and every model forecasts it. **Using it** covers
the file format, and `./predictmarketing --help` lists every option.

## The recurring job

Once installed, this is the whole loop. You do not need any of the flags below
to use it.

```
data/                  <- put your exported CSV here
data/imported/         <- it moves here once it has been read
data/reports/          <- the HTML reports land here
pm.db                  <- everything read and every forecast made, kept here
```

`data/` itself therefore shows, at a glance, exactly what has not been imported
yet: if there is a CSV sitting in it, it still needs reading.

All three live **next to the program**, not next to wherever your shell happens
to be. Run `predictmarketing import` from anywhere and it reads the same folder
and writes the same database — which matters, because `accuracy` scores the
forecasts it finds there, and a second database somewhere else would silently
have less history than you think. `-data` and `-db` override it if you want
separate ones on purpose.

### Getting the file out of Google Ads

The export has to be the right shape, and the default download is not it:

1. From the campaigns view, open the download menu and choose
   **More options** — not the one-click download.
2. Set it to **daily**. You want one row per campaign per day; a summary with
   one row per campaign has no time series in it at all.
3. Choose **`.csv`** — **not `.csv (Excel)`**.

That last one matters more than it looks. Despite the name, the Excel option is
**UTF-16 encoded and tab-separated**, so it is not a CSV in any sense the tool
can read. It is refused, with a message telling you the file looks
tab-separated. Re-download it as plain `.csv` and it loads.

If you already have an Excel-format file and would rather convert than
re-download:

```bash
iconv -f UTF-16 -t UTF-8 "Campaign report.csv" | tr '\t' ',' > fixed.csv
```

That is tested: on a real 16,485-row Excel-format export it produced a file the
tool read correctly, campaign names with commas in them included — those arrive
quoted, and quoting survives the conversion. What it would break is a campaign
name containing a **tab**, which `tr` would turn into a column break. Read the
first few lines before trusting it.

```bash
./predictmarketing import
```

That one command:

1. **Reads every new CSV in `data/`.** The folder is created for you, with a note
   inside, the first time you run it.
2. **Checks there is enough history.** At least **90 days**, or it refuses and
   tells you so. **365 days is better** — a year makes annual seasonality
   learnable. **730 days is best**: two years lets that seasonality be confirmed
   rather than guessed.
3. **Forecasts with every model**, each campaign and the account total.
4. **Writes report 1** into `data/reports/` — `chronos2` and `timesfm3` together
   on one page. This lands in seconds.
5. **Moves the CSV into `data/imported/`**, so the folder only holds what has not
   been read yet. Nothing is overwritten: a second file of the same name gets a
   timestamp.
6. **Trains the third model on your data and writes report 2** — the same two
   models plus `chronos2ft`. Training runs to completion — no time limit, so how
   long depends on your data; about 17 minutes for a two-year, fourteen-campaign
   export. **Report 1 is already on disk**, so read it while this runs.

```
05-campaigns.csv: 150 days, enough to forecast; 365 days would be better
  5 rows per day, split by "Campaign"
  forecasting: Cost, Impr., Clicks
  for 5: (account), Brand Search, Shopping - All, Performance Max, Display Remarketing
  no activity at all, not forecast: Video Awareness

  running chronos2
  running timesfm3

  report 1 of 2: data/reports/05-campaigns_models.html
  filed away:    data/imported/05-campaigns.csv

  training chronos2ft on this file, which takes a few minutes.
  Report 1 is already written -- open it while this runs.
```

### What the reports look like

Both reports are one self-contained HTML file with **every model on the same
chart**: what actually happened in grey, then one line per model over the days
ahead — the first solid, the rest dashed, each named where it ends. Report 1
has two model lines, report 2 has three. Dashes as well as colour, so the lines
are still tellable apart in greyscale or to a colour-blind reader.

Above each chart, three figures: what was actually spent over the window drawn,
and what each model expects over the days ahead. They follow the dropdowns, so
they always describe the chart you are looking at.

The forecast is drawn **five times wider than the history**, because it is the
shortest part of the series and the reason the page exists — at equal spacing it
ends up a few pixels wide with every line piled on the others. The chart opens on
the forecast and **scrolls left through the whole history**, as far back as your
data goes.

**Move the pointer across it** and a crosshair reads that day out above the
chart: the date, then every model's number with a dot on each line, or the
actual figure if you are back in the observed part. **Click** to pin a day so it
stays while you look; click again to release.

**The legend is a set of switches.** Click `actual`, `chronos2`, `timesfm3` or
`chronos2ft` to take that line off the chart — it disappears from the plot, its
end label, and the readout together. Useful when two models sit on top of each
other and you want to see one of them.

Two dropdowns choose what you are looking at:

- **Campaign** — the account total, or any one campaign
- **Metric** — spend, clicks, impressions, or whatever else your file holds

The metric list follows the file: forecast three columns and you get three
choices, forecast seven and you get seven. Under the chart is the same forecast
as numbers, one column per model, so you can see exactly where they disagree.

At the foot of every page is the provenance: each model, its weights, the
revision, the checksum, and — for the fine-tuned one — the last day it was
trained on, because a model should not be judged on days it has already seen.

If you would rather not wait for the fine-tune, `-no-finetune` writes report 1
and stops.

### Redrawing a report without forecasting again

Every forecast is kept, so the report is only a drawing of numbers already in
`pm.db`. If you want the pages rebuilt — after an upgrade that changes how the
chart looks, or because you deleted one — you do not have to import anything:

```bash
./predictmarketing report
```

That reads the database and rewrites the reports in `data/reports/`, under the
same names, in a moment. **Nothing is forecast, nothing is retrained and nothing
is written to the database** — which is the point, since retraining the third
model is the part measured in minutes.

It draws the newest run of each model, and only runs made from the same amount
of history, so the lines on a chart are always comparing like with like.

```
-series NAME    which dataset (default: every one that has forecasts)
-history N      days of past data drawn on the charts (default: all of them)
-data DIR       folder whose reports/ to write into (default: data)
-db FILE        database file (default: pm.db)
```

One difference you may notice: a percentage column loses its `%` sign. The
database stores what a rate is worth, not that the export wrote it with a sign
on, so a redrawn page cannot put it back. The numbers are the same ones. Import
the export again if you want the sign.

## Commands

| | |
|---|---|
| `predictmarketing setup` | download both models' weights and record their checksums |
| `predictmarketing import` | **the recurring job**: read new CSVs from `data/`, forecast with every model, write both reports |
| `predictmarketing report` | redraw the reports from forecasts already stored, without forecasting again |
| `predictmarketing version` | what this build is — include it in bug reports |
| `predictmarketing models` | show each model and what it can do |
| `predictmarketing forecast FILE.csv` | forecast a CSV of `date,value` rows |
| `predictmarketing runs` | list past forecasts |
| `predictmarketing accuracy` | compare past forecasts with what actually happened |

`forecast` options:

```
-model NAME     chronos2 (default) or timesfm3
-horizon N      days ahead (default 7, both models)
-history N      days of past data drawn on the chart (default 90)
-columns A,B    which columns to forecast (default: every column of numbers)
-entities A;B   which campaigns, separated by ; (default: all, plus the account
                total). Semicolons, because campaign names contain commas.
-by NAME        the column that separates campaigns, if it cannot be worked out
-future K=V,V   known-future values, e.g. -future budget=500,500,600
-series NAME    name for this series (default: the file name)
-out FILE       where to write the HTML (default: next to the CSV)
-db FILE        database file (default: pm.db)
```

## Using it

Column 1 must be the date. Everything else is worked out for you.

```csv
date,spend,impressions,clicks,conversions,revenue
2026-03-01,393.03,21799,366,15,1111.29
```

Every numeric column is forecast, and both models forecast them **together** in
one pass rather than one at a time:

```bash
./predictmarketing forecast examples/02-marketing.csv -model timesfm3 -horizon 7
```

```
timesfm3: 180 days of "02-marketing" -> 7 days ahead
  forecasting: spend, impressions, clicks, conversions, revenue
  for 1: (account)

  (account) -- spend
  day                     low         median           high
  2026-08-28           685.33         715.05         744.30
  2026-08-29           634.65         664.52         692.93
  ...

saved run 5c25d04cafc0 to pm.db
report: examples/02-marketing_forecast_timesfm3.html
```

### Campaign exports

A Google Ads export has one row per campaign per day. Point it straight at the file:

`examples/05-campaigns.csv` is one, so you can run this before you have your own:

```bash
./predictmarketing forecast examples/05-campaigns.csv -model chronos2 -horizon 7
```

```
chronos2: 150 days of "05-campaigns" -> 7 days ahead
  5 rows per day, split by "Campaign" (750 rows kept in the raw table)
  forecasting: Cost, Impr., Clicks
  for 5: (account), Brand Search, Shopping - All, Performance Max, Display Remarketing
  no activity at all, not forecast: Video Awareness
  not forecast, look like identifiers: Campaign ID
  stored, not forecast (you set these, you do not predict them): Budget
  stored but not numbers: Campaign status, Campaign, Currency code
```

You get a forecast for **each campaign and for the account as a whole**.

**Budgets, bids and targets are stored but not forecast** — they are dials you
turn, and predicting them just replays the number you set. **Rates are forecast**:
`4.20%` reads as a number, is predicted, and comes back with its sign on. At
account level a rate is the mean across campaigns, never the sum. Paused
campaigns never change, so there is nothing to forecast and they are named rather
than silently dropped. `Campaign ID` is a label, not a quantity, so it is stored
but never added up.

Everything from the original file is kept in the `raw` table for ad-hoc questions:

```sql
SELECT day, json_extract(data,'$.Campaign'), json_extract(data,'$.Cost')
FROM raw WHERE source='Campaign report'
  AND json_extract(data,'$."Campaign status"')='Enabled';
```

**Every column that holds numbers is forecast, all in one go.** Both models are
multivariate: they see spend, impressions and clicks as one system and share what
they learn across them, rather than forecasting each in isolation. Text columns are
set aside and it says so.

Add conversions, revenue, CPA, new-customer CPA — anything numeric — and they are
forecast too. Leave them out and that is fine.

Only want some of them?

```bash
./predictmarketing forecast examples/05-campaigns.csv -columns "Cost,Clicks"
```

Get a name wrong and it lists the real ones:

```
error: no column named "Costs". Columns that hold numbers: Impressions, Clicks, Cost
```

The report has a chart and a table per metric. Double-click the file it names.

**The model is in the file name**, so running both leaves you two reports side by
side to compare rather than one overwriting the other:

```
campaign report_forecast_timesfm3.html
campaign report_forecast_chronos2.html
```

### The simplest possible file

```csv
date,spend
2026-03-01,308.67
2026-03-02,412.80
```

Dates in most common formats are understood, including a UTF-8 BOM and CRLF line
endings (what Excel and Windows produce). Values may carry `£ $ €`, thousands
separators, scientific notation, or parenthesised negatives — real ad-platform
exports contain all of them. Negative values are fine; plenty of real series have them.

Rows out of order are **sorted**, so newest-first exports work as-is.

**Day/month order is decided once for the whole file.** `01/02/2026` is 1 February
to most of the world and 2 January in the US. If something in the file settles it —
any date with a component above 12 — that reading is applied throughout. If nothing
does, the file is refused rather than guessed at, because the wrong choice shifts
every observation by up to eleven months. ISO dates (`2026-01-31`) are never ambiguous.

Three things stop the import, because each of them silently changes the meaning
of your data while still producing a confident-looking forecast:

| Refused | Why |
|---|---|
| A row that cannot be read | Skipping it leaves a hole the model reads as a real dip |
| A missing day | Both models treat the series as consecutive, so a gap shifts every forecast date |
| Fewer than 32 rows | Below one input patch neither model can see a pattern |

Fill missing days in your data — a real `0` is fine — rather than leaving them out.

## Things that look wrong but are not

Three things surprise people on a first run. All three are deliberate.

**A progress bar on every command.** Every run that touches a model prints:

```
Loading weights:   0%|          | 0/170 [00:00<?, ?it/s]Loading weights: 100%|...
```

That is the model library loading, and it goes to **stderr**, not stdout. The
program's own output is clean, so piping or redirecting gives you tidy text:

```bash
./predictmarketing models 2>/dev/null        # just the models
./predictmarketing forecast data.csv 2>/dev/null > forecast.txt
```

stdout is reserved for the program, because the models talk to it over a JSON
protocol on that same stream — a stray print there would corrupt a forecast.

**Reports and the database are created private (`0600`).** They name real
campaigns and what they spend, so only your user account can read them. Sharing
one stays a deliberate act. If you need to serve a report from a web directory
or hand it to another account, change it yourself:

```bash
chmod 644 data_forecast_chronos2.html
```

**Running it leaves files behind.** Each forecast writes a report next to the
CSV and appends to `pm.db`. Nothing is hidden and nothing goes into git — they
are all ignored — but the folder does accumulate. To clear the generated files
and start fresh:

```bash
find . -name '*_forecast_*.html' -not -path './models/*' -delete
find . -name 'pm.db*' -o -name 'walkthrough.db*' | xargs rm -f
```

`find` rather than `rm *.html` on purpose: zsh, which macOS uses by default,
fails the whole command when a pattern matches nothing, and bash does not. The
form above behaves the same in both.

Deleting `pm.db` throws away every stored forecast, which is what `accuracy`
scores against. The installed models and Python environment are untouched by
the above.

## The models

| | TimesFM 3.0 | Chronos-2 | Chronos-2 fine-tuned |
|---|---|---|---|
| flag | `timesfm3` | `chronos2` | `chronos2ft` |
| Made by | Google | Amazon | Amazon + your data |
| Size | 330M params (1.2 GB) | 119M params (0.5 GB) | + 4.9 MB adapter |
| Known-future values | **no** | **yes** | **yes** |
| Licence | **non-commercial** | Apache-2.0 | Apache-2.0 |
| Ready after install | yes | yes | **no — needs your data** |

### You do not have to find the weights

**The models download themselves.** `./install.sh` fetches both, at revisions
pinned in `models/fetch.py`, and records the SHA-256 of each file. Every worker
re-checks that hash on load, so weights that were corrupted or swapped are
refused rather than used. You never visit Hugging Face, choose a file, or pick a
version.

They are **not** in this repository, for two reasons:

- **TimesFM's licence forbids it.** The TimesFM Non-Commercial License v1.0 says
  plainly: *"You will not … Distribute the TimesFM Model or a Derivative."*
  Mirroring Google's weights anywhere would breach it, so they can only come from
  Google.
- **They are too big for git.** TimesFM is a single 1,262 MB file and Chronos-2 is
  456 MB, against GitHub's hard limit of 100 MB per file.

### If Hugging Face is blocked

Chronos-2 is Apache-2.0, which permits redistribution, so it is mirrored in this
repository's [Releases](../../releases/tag/chronos-2-weights). Point `setup` at
the extracted folder:

```bash
tar -xzf chronos-2-weights.tar.gz
PM_CHRONOS2_DIR=./chronos-2 ./predictmarketing setup
```

The hash is verified exactly as a download would be — a mirror gets no more
trust than Hugging Face does. **TimesFM has no mirror and cannot have one**; on a
machine that cannot reach Hugging Face, `chronos2` will work and `timesfm3` will
not.

### The Hugging Face warning during install

Installing prints this while the weights come down:

```
Warning: You are sending unauthenticated requests to the HF Hub. Please set a
HF_TOKEN to enable higher rate limits and faster downloads.
```

**Nothing is wrong, and you do not need an account.** Both models are public and
ungated. The install that these instructions were tested against had no token set
and downloaded all 1.8 GB anonymously without trouble.

It matters only if your download is slow, stalls, or fails with a rate-limit
error. Hugging Face limits anonymous traffic per IP address, so this is most
likely on a shared or office connection, on CI, or if you install repeatedly.
If that happens, create a free **read** token at
<https://huggingface.co/settings/tokens> and set it before installing:

```bash
export HF_TOKEN=hf_...
./install.sh
```

`models/fetch.py` passes no token of its own, so `huggingface_hub` picks up
`HF_TOKEN` (or `HUGGING_FACE_HUB_TOKEN`) from the environment by itself. Nothing
in this project stores, logs or transmits it anywhere else.

If you are behind a proxy that blocks Hugging Face outright, a token will not
help — see the mirror above.

The third one is optional and trained on your own numbers:

```bash
models/.venv/bin/python models/finetune.py "your-export.csv" --steps 2000
./predictmarketing forecast "your-export.csv" -model chronos2ft
```

It runs every step it was given; there is no time limit. How long that takes
depends on how much data you have — on a two-year, fourteen-campaign export it
was about 17 minutes. It has **not** beaten the stock models on the data tried so far
(34.5% average error against 32.7%) — seven campaigns is very little to fine-tune
on. Score it with `accuracy` before relying on it.

**Known-future values** means telling the model something you already know about
the future — next week's budget, a planned promotion. The column must already be
in your CSV, so the model can see how it behaved in the past. A column used this
way becomes an *input* and is no longer forecast:

```bash
./predictmarketing forecast data.csv -model chronos2 -horizon 7 \
    -future "budget=900,900,900,900,900,900,900"
```

It works. On a series where budget drives spend, changing only the future budget
moves the forecast the way you would expect:

| next week's budget | median forecast |
|---|---|
| stays at 900 | 851 |
| doubles to 1800 | 953 |
| cut to 200 | 527 |

Name a column that is not in the CSV and it refuses, listing what is:

```
error: -future nosuch: no column named "nosuch" in data.csv (columns: spend, budget)
```

Ask TimesFM for the same thing and it refuses, loudly:

```
error: model "timesfm3" cannot use known-future values, and 1 were given.
Use a model that supports them, or drop them -- they will not be silently ignored
```

That is deliberate. A forecast that quietly ignored your budget signal is worse
than no forecast.

**The licences differ and it matters.** TimesFM 3.0 is non-commercial. If this
ever informs real spending, Chronos-2 is the one that is licensed for it.

## How it's laid out

```
main.go              the command line
ingest.go            reading CSV
db.go                SQLite: series, runs, forecasts
worker.go            talking to the models  <- the interesting one
report.go            drawing the chart
template.go          the HTML page
pm_test.go           one test per way this can go quietly wrong

models/
  timesfm3_worker.py   ~60 lines each: load the model, answer requests
  chronos2_worker.py
  fetch.py             downloads weights at pinned revisions
  requirements.txt     pinned, verified working together
  weights.json         where the weights are and their checksums
  cache/               the weights themselves (1.7 GB, not in git)
  .venv/               the Python environment (not in git)

testdata/            example CSV, vendored htmx
PLAN.md              why it is built this way
FINDINGS-onnx.md     why the models still need Python
guidelines/          the two documents that govern the code
```

## Adding a third model

One Python file, and one line in `worker.go`:

```go
var models = map[string]string{
    "timesfm3": "models/timesfm3_worker.py",
    "chronos2": "models/chronos2_worker.py",
}
```

That map is the only place in the Go code that knows a model exists. Copy a
worker, change what it loads, add the line. **No other Go file changes** — if one
has to, the design has sprung a leak.

Each worker announces itself on startup: which weights, which versions, whether
it accepts known-future values. Go asks rather than assumes, and stores that
announcement with every forecast, so any saved number can name what produced it.

## A note on htmx

htmx is embedded in the binary and inlined into every report, so the page is a
single self-contained file — it keeps working when you move, copy or email it.
(It used to reference `htmx.min.js` as a sibling file, which dangled the moment
the report left the folder it was written in.)

Be aware: htmx works by fetching things from a server, and Chrome blocks fetches
from `file://` pages. On a double-clicked report its swaps stay inert — the page
renders fully because Go bakes the chart and table in, but htmx is not doing
anything. Making it live means adding a `serve` command, which is a small change
if you want it.

## If a model gets stuck

A forecast takes about 3 seconds, whatever the horizon — almost all of it is
loading the model, not forecasting. If one has not answered after 5 minutes it is
stopped and you get an error saying so, rather than the command hanging.

```
error: model "chronos2" did not answer within 5m0s, so it was stopped.
A forecast normally takes a few seconds whatever the horizon, so this means it is
stuck rather than busy
```

## Testing

```bash
go test ./...                                   # 165 tests
go test -race -count=2 ./...                    # state leakage between tests
go test -run '^$' -fuzz FuzzReadCSV -fuzztime 60s
```

The tests are not hypotheticals: each one maps to a defect found by actually
attacking the tool. Notable ones — a newest-first CSV being read in file order, a
missing day treated as continuous, day/month ambiguity silently resolved, a
covariate history fabricated as zeros, the validator rejecting valid forecasts
over float32 noise, weights never verified against their recorded checksum, and
the report referencing an htmx file it did not ship.

## Checking the models against reality

Every forecast is kept. When the days it predicted arrive and you import a newer
export, they can be scored — nothing is recomputed, the original forecast is
still exactly as it was made.

```bash
./predictmarketing accuracy
```

```
forecast vs actual for (account)

  model      metric             days  avg error      bias  in range
  chronos2   Clicks               28      16.3%     +2.1%       61%
  chronos2   Cost                 28      13.8%     +2.2%       57%
  timesfm3   Cost                 28      11.7%     +0.1%       75%
  timesfm3   Impr.                28      17.2%    +11.8%       89%

  21 forecast days are still waiting for their actuals.
```

`-by-day` shows how accuracy decays with the horizon, `-entity` picks one
campaign, `-metric` one metric. Underneath it is the `forecast_accuracy` view, so
you can query it directly for anything the command does not cover.

## For AI agents

`AGENTS.md` is the single source of truth: what this is, how to build and test it,
the model protocol, the hard rules, and the decisions that are settled. It is
agent-agnostic — Claude, Codex, Gemini, Qwen, Cursor, or a person.

- `CLAUDE.md` and `.claude/skills/` point at it rather than restating it, so they
  cannot drift out of step. Tests enforce both the pointing and the file references.
- `examples/walkthrough.sh` runs every normal use of the tool for real.
- `examples/ground-truth.py` proves the stored numbers are the models' own.

## Building

```bash
go build -o predictmarketing .
go test ./...
```

One Go dependency: `modernc.org/sqlite`. Everything else is the standard library.
