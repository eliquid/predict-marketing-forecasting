---
name: verify
description: Prove a change to Predict Marketing is sound before claiming it works. Use after any change touching CSV reading, the model protocol, storage, or the report.
---

# Verifying a change

Read `AGENTS.md` §8 first. This skill is the longer sweep for changes that touch
the path between a CSV and a stored number.

## 1. The cheap gates (always)

```bash
gofmt -l .                 # must print nothing
go vet ./...               # must print nothing
go test -count=1 ./...     # all pass
```

## 2. Every model, end to end

```bash
./predictmarketing models        # all three, with their real capabilities
./predictmarketing forecast testdata/example.csv -model timesfm3 -horizon 3
./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 3
./predictmarketing forecast testdata/example.csv -model chronos2ft -horizon 3
```

Each must produce numbers, and each writes its own report file.

`chronos2ft` reporting "no fine-tuned model yet" is **expected on a machine where
nobody has trained one** — it is not a failure. Train it, or skip that line.

## 3. Ground truth — the check that matters most

If you touched anything between the CSV and the database, the stored numbers must
still be exactly what the library returns.

```bash
models/.venv/bin/python examples/ground-truth.py /tmp/gt.db YOUR.csv
```

It checks **every entity** — the account and each campaign — for both models.

Every line must say `EXACT`. The only permitted difference between stored output
and library output is sorting, and only where the model's own quantiles crossed.
Anything else means the pipeline is altering forecasts.

## 3a. If you touched campaign handling

```bash
./predictmarketing forecast YOUR-EXPORT.csv -model chronos2 -db /tmp/c.db
sqlite3 /tmp/c.db "SELECT COUNT(*) FROM raw"          # every input row kept
sqlite3 /tmp/c.db "SELECT DISTINCT entity FROM forecasts"   # account + campaigns
```

The campaign forecasts should sum to roughly the account forecast — they are
produced independently, so expect a few percent, not an exact match. A large gap
means something is wrong with the aggregation.

## 3b. If you touched column classification

```bash
go test -run TestColumnClassification -v .
```

It pins every real Google Ads column name to its bucket. Two traps: "Max CPC bid"
ends in "id" (match the last word, not a suffix), and CPA is an outcome, not a
setting.

## 3c. If you touched forecast storage or the accuracy view

```bash
go test -run TestAccuracy -v .
./predictmarketing accuracy -db YOUR.db -by-day
```

`actual` must be NULL for days that have not happened, and must fill in by itself
once a newer export is imported. Nothing is recomputed — the original forecast
stays exactly as it was made.

## 4. Deeper sweeps (for risky changes)

```bash
go test -race -count=2 ./...                        # state leaking between tests
go test -run '^$' -fuzz FuzzReadCSV -fuzztime 60s   # coverage-guided fuzzing
```

Concurrency:

```bash
rm -f /tmp/c.db
for s in A B C D; do (./predictmarketing forecast testdata/example.csv \
  -model chronos2 -horizon 2 -series $s -db /tmp/c.db >/dev/null 2>&1) & done; wait
sqlite3 /tmp/c.db "SELECT COUNT(*) FROM runs; PRAGMA integrity_check;"   # 4, ok
```

Runs from another directory (model paths must resolve against the binary):

```bash
cd /tmp && "$OLDPWD/predictmarketing" models
```

## 5. If you changed the report

```bash
./predictmarketing forecast testdata/example.csv -model chronos2 -out /tmp/r.html
grep -c 'src="' /tmp/r.html     # must be 0 -- the page must be self-contained
grep -c '</script' assets/htmx.min.js   # must be 0 -- inlining would break otherwise
```

## 6. What "done" means

State what you ran and what it printed. Do not say a change works because it
compiles. If a test fails, work out whether the code or the assertion is wrong
before editing either — several failures here have been stale assertions, and one
was a test helper generating `2026-01-32`.

## The import workflow

`import` touches more moving parts than any other command, so check it end to
end rather than only its units:

```bash
mkdir -p /tmp/v/data && cp examples/05-campaigns.csv /tmp/v/data/
(cd /tmp/v && /path/to/predictmarketing import -no-finetune)
```

Four things must be true afterwards:

- `data/reports/05-campaigns_models.html` exists and holds **both** models
- the CSV has moved to `data/imported/`, not been copied or deleted
- a file under 90 days is refused and **left in place**
- the report opens from `file://` with both dropdowns working

The last one needs a browser, not a grep: the dropdowns and the crosshair are
JavaScript, and a page that renders in the terminal can still be blank in a
browser. Check in the browser console that the chart scrolls
(`scrollWidth > clientWidth`), that it opens scrolled to the forecast, and that
a `mousemove` over the svg fills the readout — on a forecast day with every
model, and on a history day with the actual.

## Paths must not follow the shell

`models/`, `data/` and `pm.db` all resolve relative to the **installation**, never
to the current directory (`defaultPath` in `import.go`, `installDir` in
`worker.go`, `HERE` in each `models/*.py`). Check it after touching any of them:

```bash
cd /tmp && /path/to/install/predictmarketing import
```

It must read the install's `data/` and write the install's `pm.db`, and must not
create anything in `/tmp`. The failure this guards against is silent: a second
empty `data/` and a second database, while the models still load correctly, so
it looks like it worked. Forecasts split across databases cannot be scored, and
`accuracy` then has less history than the user believes.
