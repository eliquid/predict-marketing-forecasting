---
name: finish-an-import
description: Work out what a half-finished, interrupted or partly failed import actually left behind, and finish it without re-running the whole job. Use after a Ctrl-C, a crash, a failed fine-tune, or when data/reports/ does not hold the two reports you expected.
---

# Finishing a half-done import

`AGENTS.md` §2c is the description of the job when it goes right. This is what to
do when it stops partway. Read §2c first — the step order below is its order, and
the reason the CSV moves before training rather than after is there.

**Read this before you re-run anything.** `import` **empties the whole database**
— `forecasts`, `runs`, `series`, `raw` — on the first file of a batch, once that
file has parsed (`clearDatabase`, called from `importOne`). An import is a fresh
account, not an addition. So "re-run `import` to finish it" is now "**throw away
every forecast in the file and start over**", including the forecasts of datasets
that have nothing to do with this import. It prints what it destroyed —
`cleared 8 earlier run(s) from pm.db -- an import starts a fresh account` — but
by then it has happened. `forecast` does not wipe; only `import` does.

That is usually fine when the interrupted import is the only thing in the
database and nothing has been scored against yet. It is never fine when older
forecasts are still waiting for their actuals: those cannot be remade
(`AGENTS.md` §4a). Copy `pm.db` aside first if you are not certain.

## First: find out where it stopped

`import` leaves its state in three places. Read all three before doing anything,
because the recovery differs for each stopping point.

```bash
ls data/ data/imported/ data/reports/
sqlite3 pm.db "SELECT series_id, model, horizon, as_of, created_at
               FROM runs ORDER BY created_at;"
sqlite3 pm.db "SELECT series_id, COUNT(*) FROM series GROUP BY series_id;"
```

A finished import of one file leaves **seven runs**, in this order
(`AGENTS.md` §2c) — plus an eighth once the fine-tune lands:

```
chronos2@full  timesfm3@full  chronos2@270d  timesfm3@270d
chronos2@90d   timesfm3@90d   average@90d    [chronos2ft@full]
```

Fewer window rows is not necessarily damage: a window longer than the file, or
exactly as long as it, is skipped and said out loud. A 90-day file has only
`full`; 271 days is the first length that gives all three. What is *always*
present on a complete import is `average@90d` — it is written last of the
pretrained work and immediately before report 1, so it is the marker that the
window half finished.

| What you see | Where it stopped | What to do |
|---|---|---|
| CSV still in `data/`, no run for it | before or during the first window model | start over (see below) |
| CSV still in `data/`, some window runs, no `average@90d` | part way through the windows | start over (see below) |
| CSV still in `data/`, `average@90d` present, report 1 written | between writing report 1 and `fileAway` | move the CSV to `data/imported/` by hand, then finish the fine-tune below |
| CSV in `imported/`, only `_models.html` | at or after the fine-tune | finish the fine-tune, below |
| CSV in `imported/`, both reports | it finished | nothing |

A run row exists only once a model has finished every entity, so a model that is
half done leaves nothing. Rows in `series` and `raw` are written **before** any
model runs, so history in the database is not evidence that a forecast happened.

## If the CSV is still in `data/`

There is nothing to salvage and nothing to undo: re-running `import` wipes the
database first, so the partial runs from the first attempt go with it. Put the
CSV back in `data/` if it is not there, and run it again.

```bash
./predictmarketing import
```

What this costs is stated above: every forecast for every series, not just the
half-written ones. If the database holds anything you still want scored, copy it
somewhere first and re-run against a scratch file with `-db`.

There is no "extra `runs` row" to clean up after a re-run any more — the wipe
handles it. The delete below is only for a duplicate you made yourself with
`forecast`, which does not wipe. **Forecasts first, run second**, and see the
warning at the bottom before you do:

```bash
sqlite3 pm.db "PRAGMA foreign_keys=ON;
               DELETE FROM forecasts WHERE run_id='<the older id>';
               DELETE FROM runs      WHERE id='<the older id>';"
```

## If the CSV is already in `imported/`

Then report 1 was written and the job stopped at the fine-tune. **Do not copy the
CSV back into `data/` and re-import.** That empties the database — destroying the
six window runs and the average you already paid for — re-forecasts all of them
for nothing, and files a second, timestamped copy of the same export into
`imported/` (`audit.csv` becomes `audit-2026-09-25-140054.csv` alongside it;
`fileAway` never overwrites).

Finish it in three steps instead.

1. **Train, passing what `import` would have passed.** The command `import`
   prints on a failed fine-tune is incomplete — it omits `--steps`, and
   `finetune.py`'s own default is 1000 where `import` passes 2000. It also never
   passes `--metrics` or `--group`, which is the usual reason the fine-tune
   failed in the first place (`AGENTS.md` §2c). Read the metric and group names
   out of the import's own output, or out of the database:

   ```bash
   sqlite3 pm.db "SELECT metrics, group_by FROM runs
                  WHERE series_id='<name>' ORDER BY created_at DESC LIMIT 1;"

   models/.venv/bin/python models/finetune.py "data/imported/<file>.csv" \
       --steps 2000 --metrics "Cost,Impr.,Clicks" --group "Campaign"
   ```

   Check its first two printed lines say the series and metric counts you expect.
   `training on 1 series` when the export has campaigns means `--group` is wrong.
   See the `finetune` skill for everything else about training.

2. **Add the missing run to the same series.** The series name `import` used is
   the CSV's basename with the extension removed, and it must match exactly or
   nothing lines up:

   ```bash
   ./predictmarketing forecast "data/imported/<file>.csv" \
       -model chronos2ft -series "<name>" -horizon 7 -out /tmp/ignore.html
   ```

   **`-horizon` must be the horizon the other runs used** — the value in the
   `runs` query above. Mixed horizons under one `as_of` are what `report` cannot
   draw. `-out` is only there to keep `forecast`'s own single-model report from
   landing in `data/imported/`.

   Give it the **whole file**, not a trimmed window: the fine-tune is the one
   model trained on the data, and `import` runs it over the full history
   (`AGENTS.md` §2c). One difference from a clean import: `forecast` stores the
   bare model name, so this run lands as `chronos2ft`, where `import` would have
   stored `chronos2ft@full`. `report` draws it either way, but `accuracy` groups
   by that column, so it will be scored as its own model rather than pooled with
   the fine-tunes of other imports.

3. **Redraw.** `report` writes report 2 as soon as a run declaring
   `trained_through` is stored:

   ```bash
   ./predictmarketing report
   ```

   It rewrites both pages from the stored runs and prints what went on each. On
   a database from a current import that is:

   ```
   data/reports/<name>_models.html         (average@90d)
   data/reports/<name>_with-finetune.html  (average@90d, chronos2ft)
   ```

   Six window runs are in the database and none of them is drawn — that is
   correct, not a missing line (`AGENTS.md` §2c). If report 1 comes back naming
   every `chronos2@…` and `timesfm3@…` instead, this database has no
   `average@90d` and the windows never finished.

## Before you trust `data/reports/`

Nothing deletes a report, and the pages carry no date in their heading, so a
`_with-finetune.html` from an earlier export survives untouched beside a fresh
`_models.html` and looks like its pair. If the fine-tune failed or `-no-finetune`
was used, clear the stale one rather than reading it:

```bash
rm -f "data/reports/<name>_with-finetune.html"
```

`import` exits 0 when the fine-tune fails, so a script that only checks the exit
code cannot tell a one-report import from a two-report one. Check for the file.

## Resetting the data side to a clean slate

`import` already empties everything, so this is only for clearing **one** dataset
out of a database you want to keep the rest of. Only when you actually want that
history gone — `accuracy` scores against it, and it cannot be rebuilt from an
export you no longer have (`AGENTS.md` §4a):

```bash
sqlite3 pm.db "PRAGMA foreign_keys=ON;
  DELETE FROM forecasts WHERE run_id IN (SELECT id FROM runs WHERE series_id='<name>');
  DELETE FROM runs      WHERE series_id='<name>';
  DELETE FROM series    WHERE series_id='<name>';
  DELETE FROM raw       WHERE source='<name>';"
find data/reports -name '<name>_*.html' -delete
```

**The order is not optional, and neither is the pragma.** `forecasts.run_id`
references `runs(id)` with no `ON DELETE` clause (`AGENTS.md` §4a), so:

- with `foreign_keys` on — which is what the program itself always uses — a
  `DELETE FROM runs` that still has forecasts is **refused** with
  `FOREIGN KEY constraint failed`;
- with it off, which is the `sqlite3` CLI's default, the same delete **succeeds
  and leaves the forecast rows orphaned**. Nothing notices: `report` and
  `forecast_accuracy` both join `runs`, so orphans are invisible to every
  command while still occupying the file.

If a run has already been deleted the careless way, that is the only way to find
what is left:

```bash
sqlite3 pm.db "SELECT COUNT(*) FROM forecasts f
               LEFT JOIN runs r ON r.id = f.run_id WHERE r.id IS NULL;"
sqlite3 pm.db "DELETE FROM forecasts WHERE run_id NOT IN (SELECT id FROM runs);"
```

Deleting `pm.db` itself throws away every other dataset too, which is almost
never what is wanted.
