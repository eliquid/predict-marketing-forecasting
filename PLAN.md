# Predict Marketing

A Go program that does what the two model repositories do — with either
Google's TimesFM 3.0 or Amazon's Chronos-2 behind it.

Standalone: it vendors nothing from either model's own repository and talks to
both through the same small adapter.

---

## What the two repos actually give you

Strip away the notebooks and the READMEs and both repos are the same three lines:

```python
pipeline = Chronos2Pipeline.from_pretrained("amazon/chronos-2")
quantiles, mean = pipeline.predict_quantiles(series, prediction_length=7,
                                             quantile_levels=[0.1, ..., 0.9])
```

```python
f = timesfm.TimesFM3Forecaster.from_pretrained("google/timesfm-3.0-pytorch")
out = f.predict(context=series, horizon=7, return_quantiles=True)
```

Load weights. Hand it numbers. Get back a forecast with uncertainty bands.
That is the whole surface area. Everything else in those repos is packaging.

## What this project is

One Go program with one function, that works with either of them:

```go
q, err := pm.Forecast(series, horizon, quantiles)
```

Plus the things the repos leave to you: somewhere to keep the data (SQLite), a
chart you can actually look at, and a record of what produced each forecast.

**What is written in what, honestly:**

- **Go** — everything you touch: the command, CSV reading, the database, the chart,
  the HTML. Roughly 1,500 lines.
- **Python** — about **120 lines total**, 60 per model. A thin adapter so Go can
  talk to each one.
- **Not written by me at all** — the models. Google's and Amazon's code and weights,
  downloaded and run unmodified.

It is not "100% Go", and earlier drafts of this plan said so incorrectly. A Go
program can drive `ffmpeg` without being written in C; this is the same shape,
except the ~120 lines of glue are mine too.

Running the models *without* Python was tested properly and does not work for both
— see FINDINGS-onnx.md. Chronos-2 has no ONNX path at all. Settled; not revisited.

## How either model plugs in

Each model gets a small Python file that does nothing but load it and answer
requests. Go starts it and talks to it over JSON, one message per line.

```go
var models = map[string]string{
    "timesfm3": "workers/timesfm3.py",
    "chronos2": "workers/chronos2.py",
}
```

That map is the only place in the Go code that knows a model exists. Switching
models is a flag: `-model chronos2`. Adding a third is one Python file and one
line here — no Go changes.

Each worker announces what it can do when it starts (which version, which
weights, whether it accepts future-known values like budget). Go asks rather
than assumes, and stores the answer with every forecast.

---

## What's in it

    go.mod
    main.go          the command
    db.go            SQLite
    ingest.go        CSV in
    worker.go        talking to the models
    report.go        writes the HTML file
    templates/       HTML + vendored htmx.min.js
    workers/timesfm3.py   ~60 lines
    workers/chronos2.py   ~60 lines
    testdata/

Three tables:

    series    (series_id, day, value)
    runs      (id, series_id, model, horizon, created_at, model_info)
    forecasts (run_id, day, quantile, value)

One dependency: `modernc.org/sqlite`. Everything else is Go's standard library.
htmx is a vendored file sitting next to the HTML, not a CDN, so it works offline.

## What it does when it's finished

One command:

    predictmarketing forecast data.csv -model chronos2 -horizon 7

It writes two things: rows in the SQLite file, and `forecast.html` next to your
data. Double-click the HTML — chart of the next N days with a shaded band, and
the numbers in a table underneath.

No server. No localhost. It's a file on your hard drive, self-contained, opens
offline, survives being emailed or moved. Same as the HTML your other setup
writes today.

htmx ships in the page, vendored alongside it. The chart is inline SVG.

One thing to know so it isn't a surprise later: htmx fetches things, and Chrome
blocks fetches from `file://` pages. So on a double-clicked file its swaps stay
inert — it's there and ready, but the page renders from what Go already baked in.
Say the word and I'll add a `serve` command back, at which point htmx goes live.

---

## Getting it onto other computers

This is a requirement, not an afterthought, so it is a step with its own check.

Three things have to land on a target machine:

| | How | Size |
|---|---|---|
| The program | Cross-compiled Go binary. `modernc.org/sqlite` is pure Go with no CGo, so `GOOS=linux go build` just works — one command per platform, no toolchain on the target. | ~15 MB |
| The Python side | `uv sync --frozen` against a committed `uv.lock`, one per model | ~700 MB |
| The weights | `predictmarketing setup` downloads them and **verifies sha256 against a pinned revision** before use | 1.2 GB + 0.5 GB |

So a new machine is two commands:

    predictmarketing setup                     # env + weights + checksum verify
    predictmarketing forecast data.csv -model chronos2 -horizon 7

`setup` refuses to proceed on a checksum mismatch rather than forecasting with
weights it can't identify. Same instinct as everything else here: fail loudly.

If a target isn't a Mac, or you want one artefact instead of two commands, a
container image is the fallback — heavier, but it makes the Python side someone
else's problem.

---

## Steps

| # | Step | Done when |
|---|------|-----------|
| 1 | Go project + SQLite tables | `go test` creates the DB and round-trips a series |
| 2 | CSV in | A fixture file loads and reads back identical |
| 3 | The model protocol + TimesFM worker | First forecast comes out of Go |
| 4 | `forecast` command, saved runs | Same input twice = same answer, and each run records which model and version made it |
| 5 | The HTML report | Double-clicking the file shows the chart and table |
| 6 | Chronos worker | Dropdown has two entries, and no Go file changed to add it |
| 7 | Future-known values | Chronos uses them. TimesFM refuses out loud instead of ignoring them. |
| 8 | `setup` + lockfiles + cross-compile | **It runs on a second computer** from a clean copy, and a corrupted weights file is caught by the checksum instead of forecasting |

## What has to be downloaded, and when

| When | What | Size |
|------|------|------|
| Step 1 | `modernc.org/sqlite` (Go library) | small |
| Step 3 | TimesFM 3.0 weights + the `timesfm` Python package | 1.2 GB |
| Step 6 | Chronos-2 weights + the `chronos-forecasting` package | ~0.5 GB |

Steps 1–2 need nothing but Go, which you already have.

Each model gets its own isolated Python environment inside this project. Nothing
is shared with or taken from anywhere else on your Mac.

---

## Two facts that affect which model you use

**Licences differ, and this now matters more.** TimesFM 3.0 is non-commercial.
Chronos-2 is Apache-2.0. Once this is running on other people's computers and
informing real spending, that is the deciding factor — and it points at Chronos-2
as the default model, with TimesFM as the comparison.

**Only Chronos-2 accepts future-known values.** You can tell it "next week's
budget is £500" and it will use it. TimesFM only looks backwards.

---

## Rules I'll hold to

- A model that can't do something says so and fails. Never silently ignores input.
- Every forecast is checked before it's stored: right shape, no NaN or infinity,
  bands in ascending order. Negatives are allowed -- they are real data.
- Covariate history comes from the CSV, never invented.
- Rows are sorted; gaps and too-short series are refused, not patched over.
- Bad rows in a CSV are named, never guessed at or filled in with a default.
- Deliberate shortcuts get a `ponytail:` comment saying what the limit is.

## Shortcuts taken on purpose

- One model process at a time, requests queued. Fine for one person.
- JSON between Go and Python. The data is a few thousand numbers — not a bottleneck.
- Hand-drawn SVG chart, no chart library. Add one only if you want hover and zoom.
