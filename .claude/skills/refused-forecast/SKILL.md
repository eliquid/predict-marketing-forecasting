---
name: refused-forecast
description: Diagnose a forecast the tool refused, repaired, or never returned. Use when a run dies with "returned an unusable forecast", when a model command hangs with no output, or when stored quantiles look mislabelled.
---

# A forecast that was refused, repaired, or never came back

Read `AGENTS.md` §4 first — it defines the protocol, and §5 the validation rules
this is the debugging side of. `AGENTS.md` §2a1 records the real incident that
makes this a recurring job: one long-dead campaign came back 6.9% out of order,
failed validation, and took a whole import down with it.

Nothing here is a fix. Work out which of the four causes you have, then change
the thing that is actually wrong.

## Read the message properly first

```
chronos2, Patches DSA #2 - Zombie: model "chronos2" returned an unusable
forecast for "Cost": day 0: quantiles are badly out of order (largest crossing
44.4% of the forecast's own size, far beyond the ~0.2% a model normally produces)
```

Three things it does not say, and one it says misleadingly:

- **`day 0` is an offset into the horizon, not a date.** Day 0 is the first
  forecast day — tomorrow relative to `as_of`, not the last day of history.
- **The entity comes from the caller**, not the validator. In `import` it is the
  `model, entity:` prefix (with the CSV's filename in front of that); in
  `forecast` it is the bare `entity:` prefix. A message with no prefix came from
  somewhere that lost it.
- **It does not say which window.** `import` forecasts the file three times —
  `full`, `270d`, `90d` (`AGENTS.md` §2c) — but `runModel` names the model by its
  bare name, not its `chronos2@90d` run label, so the message is the same from
  any of them. The last `  270d window (270 days)` line printed above it is what
  tells you. The same entity can be refused in one window and fine in another:
  the shorter the window, the less of the campaign's live history it contains.
- **Nothing is stored for the failed run.** A failed forecast fails the whole
  run, so there is no half-written row to clean up.
- **"quantiles are badly out of order" blames the forecast**, and the forecast is
  not always what is wrong. See cause 3.

**What a refusal inside `import` costs you now.** `import` empties the whole
database — `forecasts`, `runs`, `series`, `raw` — before the first model runs,
and report 1 is written only after every window *and* the average have finished
(`AGENTS.md` §2c). So a refusal leaves: the old database **gone**, this export's
`series` and `raw` stored, some window runs stored, and **no report at all**.
Report 1 is never already on disk when a model refuses; the only page that can
survive is one from an earlier import, which is now stale beside an emptied
database. Diagnose with `forecast -db /tmp/d.db` (which wipes nothing) and only
re-run `import` once the cause is fixed.

## Cause 1 — the entity has nothing to forecast

By far the most common, and the one `AGENTS.md` §2a1 is about. A campaign that
stopped spending comes back as float noise, and noise has no reliable ordering.

```bash
# is it switched off in the export?
./predictmarketing forecast YOUR.csv -model chronos2 -entities "THE NAME" -db /tmp/d.db
```

If it is switched off, that message tells you so instead of forecasting. If it is
switched *on* and dead anyway, the export's status column is not being believed —
check the column is found by its values, not its name (`campaignStates` in
`ingest.go`, §2a1), and that `CAMPAIGN_STATES` in `models/finetune.py` still
matches it.

## Cause 2 — the crossing is real and the model is struggling

Re-run the same entity alone and look at what comes back:

```bash
./predictmarketing forecast YOUR.csv -model chronos2 -entities "THE NAME" \
  -horizon 7 -db /tmp/d.db
```

`forecast` prints a note when it repaired a crossing; `import` does not report
one at all, so a single-entity `forecast` is the only way to see the size of a
repair. If the refused metric is near zero for the whole window, you are back at
cause 1.

## Cause 3 — the worker's declared quantiles do not match its reply

Only reachable when someone has added or edited a worker, and it is the cause the
message hides. `checkForecast` sorts each day ascending as its repair, and the
stored quantile *label* comes from the handshake's declared order by position. So
a worker that declares `[0.5,0.1,0.9]`, or that returns its columns in an order
other than the one it declared, is either refused with a message about the
forecast (wide intervals) or silently relabelled (narrow ones).

```bash
./predictmarketing models                 # the declared list, per model
models/.venv/bin/python models/NAME_worker.py < one-request.jsonl | head -2
```

The declared list must be ascending, and the reply's columns must be in that same
order. Chronos-2 honours `quantile_levels` in whatever order it is handed, so it
will not correct a mistake for you; TimesFM 3.0 refuses any list but its own nine.

**The report will not show you this.** Every drawn line now carries a shaded
q10–q90 band, but `compare.go` takes the band from the *positions* `q[0]` and
`q[len-1]` and the median from the middle position — and `checkForecast` has
already sorted each day ascending. So the page draws a correct-looking band from
values whose stored labels are wrong. It surfaces in `accuracy` instead, where
`forecast_accuracy` joins `low` and `high` on the literal values 0.1 and 0.9:
a mislabelled grid reports `in range 0%`, or an interval far too narrow or wide
for the model.

## Cause 4 — no answer at all

A model command that sits silent is not always slow. The 5-minute timeout covers
the *reply* only; the handshake read has no deadline, so a worker that stalls
while loading hangs forever with nothing after the library's progress bar.

- Output stops right after `Loading weights: …` and never returns → the worker is
  stuck before its handshake. `Ctrl-C` and run the worker by hand:
  `models/.venv/bin/python models/NAME_worker.py < /dev/null` — it should print
  one JSON line and exit.
- The message is "did not answer within 5m0s … stuck rather than busy" → the
  handshake worked and the forecast itself hung. The worker has been killed;
  the process is gone, not leaked.
- "exited before saying hello" or "could not start: …" → the worker's own last
  explanation is in the message. Read it; it is usually a missing adapter, a
  missing environment, or a failed weights checksum.

## Do not do these

- **Do not widen `absurdCrossing`** to make a refusal go away. 5% is far beyond
  the ~0.2% both models actually produce; a forecast past it is broken, and the
  rule is in `AGENTS.md` §5.
- **Do not lower the `1e-6` floor** in `checkForecast` to catch something. It is
  absolute and in the CSV's own units, and lowering it re-opens the defect in
  `AGENTS.md` §9 where a flat-zero series was refused for crossing "10%" between
  two values a billionth apart.
- **Do not skip the entity.** `AGENTS.md` §5: unusable input is named, never
  quietly dropped. If an entity should not be forecast, it should be excluded by
  the status rule (§2a1), where the reason is recorded and printed.
