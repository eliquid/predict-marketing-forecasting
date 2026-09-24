---
name: finish-an-import
description: Work out what a half-finished, interrupted or partly failed import actually left behind, and finish it without re-running the whole job. Use after a Ctrl-C, a crash, a failed fine-tune, or when data/reports/ does not hold the two reports you expected.
---

# Finishing a half-done import

`AGENTS.md` §2c is the description of the job when it goes right. This is what to
do when it stops partway. Read §2c first — the step order below is its order, and
the reason the CSV moves before training rather than after is there.

## First: find out where it stopped

`import` leaves its state in three places. Read all three before doing anything,
because the recovery differs for each stopping point.

```bash
ls data/ data/imported/ data/reports/
sqlite3 pm.db "SELECT series_id, model, horizon, as_of, created_at
               FROM runs ORDER BY created_at;"
sqlite3 pm.db "SELECT series_id, COUNT(*) FROM series GROUP BY series_id;"
```

| What you see | Where it stopped | What to do |
|---|---|---|
| CSV still in `data/`, no run for it | before or during a model | re-run `import` (see below) |
| CSV still in `data/`, one model has a run | between the two models | re-run `import` (see below) |
| CSV in `imported/`, only `_models.html` | at or after the fine-tune | finish the fine-tune, below |
| CSV in `imported/`, both reports | it finished | nothing |

A run row exists only once a model has finished every entity, so a model that is
half done leaves nothing. Rows in `series` and `raw` are written **before** any
model runs, so history in the database is not evidence that a forecast happened.

## If the CSV is still in `data/`

Re-run `import`. Nothing needs undoing: `series` upserts on
`(series_id, entity, metric, day)` and `raw` is replaced per source, so the
second attempt overwrites rather than doubles (`AGENTS.md` §4a). The only residue
is an extra `runs` row for whichever model got through the first time — harmless
to `report`, which takes the newest run of each model, but it does appear a
second time in `accuracy`'s row counts. Delete it if that matters — **forecasts
first, run second**, and see the warning at the bottom before you do:

```bash
sqlite3 pm.db "PRAGMA foreign_keys=ON;
               DELETE FROM forecasts WHERE run_id='<the older id>';
               DELETE FROM runs      WHERE id='<the older id>';"
```

## If the CSV is already in `imported/`

Then report 1 was written and the job stopped at the fine-tune. **Do not copy the
CSV back into `data/` and re-import**: that re-forecasts both pretrained models
for nothing and files a second, timestamped copy of the same export into
`imported/`.

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

3. **Redraw.** `report` writes report 2 as soon as a run declaring
   `trained_through` is stored:

   ```bash
   ./predictmarketing report
   ```

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

Only when you actually want the history gone — `accuracy` scores against it, and
it cannot be rebuilt from an export you no longer have (`AGENTS.md` §4a):

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
