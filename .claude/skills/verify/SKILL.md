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
./predictmarketing forecast testdata/example.csv -model timesfm3 -horizon 3 -db /tmp/v.db
./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 3 -db /tmp/v.db
./predictmarketing forecast testdata/example.csv -model chronos2ft -horizon 3 -db /tmp/v.db
```

Each must produce numbers, and each writes its own report file.

**Give every verification run a scratch `-db`.** `forecast` calls `saveRun`
before it writes the report, and the default database is the **installation's**
`pm.db` — so a check you ran to see whether the code still compiles becomes a
run that `accuracy` scores against real actuals later (`AGENTS.md` §2c).

`chronos2ft` reporting "no fine-tuned model yet" is **expected on a machine where
nobody has trained one** — it is not a failure. Train it, or skip that line.

## 3. Ground truth — the check that matters most

If you touched anything between the CSV and the database, the stored numbers must
still be exactly what the library returns.

The script reads the runs out of the database, so the two forecasts have to be
stored in it first — on an empty or missing file it dies with
`sqlite3.OperationalError: no such table: runs`.

```bash
rm -f /tmp/gt.db
./predictmarketing forecast YOUR.csv -model chronos2 -db /tmp/gt.db
./predictmarketing forecast YOUR.csv -model timesfm3 -db /tmp/gt.db
models/.venv/bin/python examples/ground-truth.py /tmp/gt.db YOUR.csv
```

It checks **every entity that was forecast** — the account and each switched-on
campaign — for both models.

Every line must say `EXACT`. The only permitted difference between stored output
and library output is sorting, and only where the model's own quantiles crossed.
Anything else means the pipeline is altering forecasts.

## 3a. If you touched campaign handling

```bash
./predictmarketing forecast YOUR-EXPORT.csv -model chronos2 -db /tmp/c.db
sqlite3 /tmp/c.db "SELECT COUNT(*) FROM raw"                # every input row kept
sqlite3 /tmp/c.db "SELECT DISTINCT entity FROM series"      # account + EVERY campaign
sqlite3 /tmp/c.db "SELECT DISTINCT entity FROM forecasts"   # account + switched-on only
```

The two entity lists are **meant to differ**: everything is stored, only the
campaigns the export says are switched on as of its last day are forecast
(`AGENTS.md` §2a1). `series` short of a campaign that is in `raw` is a bug;
`forecasts` short of a paused one is not.

The campaign forecasts should sum to roughly the account forecast — they are
produced independently, so expect a few percent, not an exact match. A large gap
means something is wrong with the aggregation. Note the account total is the sum
of *all* campaigns including the paused ones, so a file with paused spend in it
will not add up from the forecast entities alone.

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
once those days arrive in a newer file. Nothing is recomputed — the original
forecast stays exactly as it was made.

**Prove that with `forecast`, never with `import`.** `import` empties the whole
database before it stores anything (`AGENTS.md` §4a), so the forecast you were
about to score is deleted by the very run that brings its actuals. Forecast the
older file and then the newer one into the same scratch `-db`:

```bash
rm -f /tmp/acc.db
./predictmarketing forecast OLDER.csv -model chronos2 -series X -db /tmp/acc.db -out /tmp/a1.html
./predictmarketing accuracy -db /tmp/acc.db            # actual NULL, "still waiting"
./predictmarketing forecast NEWER.csv -model chronos2 -series X -db /tmp/acc.db -out /tmp/a2.html
./predictmarketing accuracy -db /tmp/acc.db -by-day    # the same rows now scored
```

Read `days` before reading `in range`: it is a row count, not a date range, and
`-by-day` splits a single 7-day backtest into seven rows of one, where every cell
prints 0% or 100% (`AGENTS.md` §4a).

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
./predictmarketing forecast testdata/example.csv -model chronos2 -out /tmp/r.html -db /tmp/v.db
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
./predictmarketing import -no-finetune -data /tmp/v/data -db /tmp/v/pm.db
```

`-data` and `-db` are not optional here, and `-db` is now the dangerous one.
`cd`-ing into `/tmp/v` and running `import` with no flags reads the
**installation's** `data/` — that is the whole point of `defaultPath`, checked at
the end of this file — and **empties the installation's `pm.db`** on the way
(`AGENTS.md` §4a). A verification run must never be given the real database.

Five things must be true afterwards:

- `/tmp/v/data/reports/05-campaigns_models.html` exists and draws exactly **one**
  forecast line, `average@90d`, with its q10–q90 band behind it
- every window run is **stored** even though only one line is drawn
- the CSV has moved to `/tmp/v/data/imported/`, not been copied or deleted
- a file under 90 days is refused and **left in place**
- the report opens from `file://` with both dropdowns working

```bash
sqlite3 /tmp/v/pm.db "SELECT model, as_of FROM runs ORDER BY model;"
grep -o 'data-model="[^"]*"' /tmp/v/data/reports/05-campaigns_models.html | sort -u
```

On this file `runs` holds five rows — `chronos2@full`, `timesfm3@full`,
`chronos2@90d`, `timesfm3@90d` and `average@90d` — while the page carries
`__actual` and `average@90d` and nothing else (`AGENTS.md` §2c).
`05-campaigns.csv` is 150 days, so the 270d window is skipped and announced;
a file over 271 days gives six window runs instead of four. Only
`..._with-finetune.html` adds a second line, `chronos2ft@full`, and only when the
run was not given `-no-finetune`.

**A band per drawn line.** Every drawn series is shaded q10–q90 behind it, inside
the same group so the legend hides both together. The page above has 5 entities x
3 metrics, so `grep -c 'fill-opacity="0.13"'` is 15 — one band, not six.

**The dropdown check needs a browser, not a grep:** the dropdowns and the
crosshair are JavaScript, and a page that renders in the terminal can still be
blank in a browser. Check in the browser console that the chart scrolls
(`scrollWidth > clientWidth`), that it opens scrolled to the forecast, and that
a `mousemove` over the svg fills the readout — on a forecast day with the
`average@90d` line, and on a history day with the actual. Look at the shaded
band too: it is drawn, not grepped, and a band on the wrong axis or inverted
still passes every string check above.

Only the pane visible at load opens scrolled to the forecast; every pane reached
through a dropdown opens at the oldest day (`AGENTS.md` §2c). That is the
current behaviour, not a regression you introduced.

A synthetic `mousemove` is not enough on its own. The first crosshair put the
readout **inside** the scroller, where it scrolled out of sight with the content
and showed the user nothing, while exactly that kind of test found it in the DOM
and passed. Look at the page.

### Iterating on the drawing: `report`, not `import`

Once one import has stored its runs, do not import again to see a chart change.
It is no longer merely wasteful: the second import empties the database it is
about to refill, and takes the fine-tune with it.

```bash
./predictmarketing report -data /tmp/v/data -db /tmp/v/pm.db
```

It redraws `data/reports/` from the database — nothing read, nothing forecast,
nothing retrained, nothing written (`AGENTS.md` §2c). Two things to check after
touching it:

- it refuses cleanly on a database with no stored runs, naming `import`
  (`error: no stored forecasts in /tmp/empty.db. Run predictmarketing import first`)
- a rate loses its `%` sign, and only that; if a *number* differs from the page
  `import` wrote, the rebuild in `rebuildData` is wrong, not the renderer

**`report` redraws the comparison pages only.** Nothing in `rerender.go` calls
`writeReport`, so if what you changed is `report.go`, `template.go` or
`format.go` — the single-model page — running `report` proves nothing at all: it
rewrites `data/reports/*.html` with none of your change in them. Re-run
`forecast` with a scratch `-db` instead (`AGENTS.md` §2c).

## Paths must not follow the shell

`models/`, `data/` and `pm.db` all resolve relative to the **installation**, never
to the current directory (`defaultPath` in `import.go`, `installDir` in
`worker.go`, `HERE` in each `models/*.py`). Check it after touching any of them —
**with the install's `data/` holding no pending CSV**:

```bash
cd /tmp && /path/to/install/predictmarketing import
```

With nothing waiting, `pendingFiles` refuses before the database is even opened
and names the folder it looked in:

```
error: no CSV files in /path/to/install/data/.
```

(Or `created /path/to/install/data/ for you, and it is empty` if the folder does
not exist yet — same proof, and it creates the folder at the install, not in
`/tmp`.) That path *is* the proof, and no database file was created.

**Never run this check with an export sitting in the install's `data/`**:
`import` would forecast it for real and empty the install's `pm.db` first
(`AGENTS.md` §4a). The failure this guards
against is silent: a second empty `data/` and a second database, while the models
still load correctly, so it looks like it worked. Forecasts split across databases
cannot be scored, and `accuracy` then has less history than the user believes.
