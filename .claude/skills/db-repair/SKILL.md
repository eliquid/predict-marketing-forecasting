---
name: db-repair
description: Diagnose and repair a Predict Marketing database that is refused, unreadable, or holding numbers it should not. Use when a command dies on open, when `accuracy` reports nothing on a database that has runs in it, or when a corrected export has been imported over a wrong one.
---

# Repairing a database

Read `AGENTS.md` §4a first for what the schema is and why it is shaped that way.
This skill is only the procedures for getting a file back into a state the tool
can read correctly. It does not tune anything — that is `sqlite-optimization`.

**Work on a copy.** Every repair here is a delete.

```bash
cp pm.db /tmp/repair.db
```

**Nothing in this database is irreplaceable except the forecasts.** `raw` and
`series` are derived from the CSV exports and can be rebuilt by re-importing
them. `runs` and `forecasts` cannot — they are predictions made before the days
happened, and re-running the model today does not reproduce them. When a repair
forces a choice, keep `runs` and `forecasts`.

**Which is why no repair here runs `import`.** `import` empties `forecasts`,
`runs`, `series` and `raw` before it stores anything (`clearDatabase`, called
from `importOne` — an import is a fresh account, `AGENTS.md` §2c). Running it to
"rebuild" a damaged table destroys the one part of the file that cannot be
rebuilt. Where a repair below needs data re-read from a CSV it uses `forecast
-series NAME`, which writes the same `series` and `raw` rows and touches nothing
else.

Start here, always:

```bash
sqlite3 /tmp/repair.db "PRAGMA integrity_check; PRAGMA foreign_key_check;
  SELECT 'user_version', * FROM pragma_user_version();
  SELECT 'journal', * FROM pragma_journal_mode();
  SELECT 'runs', COUNT(*) FROM runs;
  SELECT 'forecasts', COUNT(*) FROM forecasts;
  SELECT 'series', COUNT(*) FROM series;
  SELECT 'raw', COUNT(*) FROM raw;"
```

---

## 1. It refuses to open

### "predates per-entity storage and cannot be read by this build"

Working as designed — `checkSchema` caught a file older than the current schema
and named the table and column. There is no migration and there is not meant to
be one (`AGENTS.md` §4a). Point `-db` at a new file and re-import the exports.
If the old file holds forecasts worth keeping, they can be copied across by hand
only if `runs` and `forecasts` already have the current columns; check with
`PRAGMA table_info(forecasts)` before spending time on it.

### "was written by a newer version of this tool"

The `user_version` stamp is ahead of this build's `schemaVersion`. Use the newer
build. Do not lower the stamp — the file's shape is what the refusal is about,
and forcing it past the gate is how you get `no such column` later.

### "is in WAL mode, and WAL needs to create a ...-shm file beside it"

```
error: pm.db is in WAL mode, and WAL needs to create a pm.db-shm file beside it,
which this directory does not allow. Copy the database somewhere writable and
open it there
```

`openDB` recognises this case now and says it outright. Older builds let SQLite's
own `attempt to write a readonly database (1544)` through, reported against
`reading schema version of pm.db`, which read like corruption. It is not: SQLite
must create the `-shm` sidecar before it can read a WAL database at all, so the
open fails on the first statement.

A read-only *file* in a writable directory is fine and always has been. A
read-only *directory* — a mounted snapshot, a share, a locked backup volume — is
not.

Repair, **on writable storage, before the file is moved**:

```bash
sqlite3 pm.db 'PRAGMA journal_mode=DELETE'
```

A DELETE-journal database needs no sidecar and opens read-only unchanged.
`openDB` puts it back into WAL the first time it is opened somewhere writable, so
this is a property of the copy you hand out, not of the database.

---

## 2. It opens, and answers nothing

`accuracy` printing `no forecast day has an actual yet` and exiting 0 on a
database that visibly has runs in it means the *view* is stale, not the data.

```bash
sqlite3 /tmp/repair.db "SELECT sql FROM sqlite_master WHERE name='forecast_accuracy';"
sqlite3 /tmp/repair.db "SELECT sql FROM sqlite_master WHERE name='forecasts_median';"
```

**Fixed in the code, so on a current binary this repairs itself.** `openDB` calls
`refreshDerived`, which compares the view and both indexes against
`derivedObjects` in `db.go` on every open and rebuilds any that differ. Simply
running any command against the file is the repair.

It is worth knowing what it looked like, because a database that has not been
opened by a current binary still has it. The view and indexes used to sit inside
`schema` behind `CREATE ... IF NOT EXISTS`, which does nothing on a file that
already has the object — so a database created by an earlier build kept that
build's view forever, and `staleTable` could not notice, because it only inspects
table columns. A file already stamped at the current `schemaVersion` never had the
DDL run over it at all.

Repair, if you are on an older binary:

```bash
sqlite3 /tmp/repair.db "DROP VIEW IF EXISTS forecast_accuracy;
                        DROP INDEX IF EXISTS forecasts_median;"
```

then open the file with the tool once. Check the plan afterwards, because an index that is present but
not used looks identical from the outside:

```bash
sqlite3 /tmp/repair.db "EXPLAIN QUERY PLAN
  SELECT COUNT(*) FROM forecast_accuracy WHERE entity='(account)';"
```

The first line must name `forecasts_median`. Measured on a database written by
the current build, it is:

```
|--SEARCH f USING COVERING INDEX forecasts_median (entity=?)
```

`SEARCH f USING INDEX forecasts_median (entity=?)` — without `COVERING` — is
equally right; whether SQLite can answer from the index alone depends on the
columns asked for. `SCAN f USING INDEX forecasts_median` is also fine, it visits
only the median rows, and is what a query with no entity filter gets. A bare
`SCAN forecasts` is the failure `AGENTS.md` §4a warns about.

**Before changing the view or the index yourself**, note this is the reason a
`schemaVersion` bump is not enough: put the `DROP … IF EXISTS` ahead of the
`CREATE` in `schema`, or the change reaches new databases only — which is where
the tests look, so nothing will fail.

---

## 3. `accuracy` is scoring days the current export does not contain

**Fixed in the code, so this is for databases written before that.** `saveData`
now deletes the whole `series_id` before inserting, the way `saveRaw` always did,
so the two tables agree by construction and a re-import — including one for an
entirely different account under the same name — replaces rather than merges.

Older files can still hold the damage. Until that change `saveData` was an upsert
with no delete, so a re-import that *narrowed* an export left `raw` equal to the
newest file and `series` equal to the union of every import ever done under that
name. `forecast_accuracy` joins to `series`, never to `raw`, so those disowned
rows went on being scored as actuals with nothing in the output to mark them.
Re-importing once with a current binary clears it; the query below tells you
whether you need to.

Detect. `raw.source` and `series.series_id` are both the `-series` name (the file
basename when nobody said), and nothing enforces that they agree:

```bash
sqlite3 -header -column /tmp/repair.db "
SELECT s.series_id, s.entity, COUNT(DISTINCT s.day) AS orphan_days,
       MIN(s.day) AS first, MAX(s.day) AS last
FROM series s
WHERE NOT EXISTS (SELECT 1 FROM raw r
                  WHERE r.source = s.series_id AND r.day = s.day)
GROUP BY 1, 2;"
```

Empty means clean. Any row means `series` is claiming days the export disowns.

That query finds orphan *days*. It does not find a campaign whose days still
exist but whose numbers were never corrected — which happens when the newer
export has one campaign per day and so produces no campaign split at all, leaving
the per-campaign entities frozen at the older import's values. If the shape of the
export changed, assume the whole series is suspect rather than trusting the query.

Repair — re-read the corrected CSV under the same name **with `forecast`, not
`import`**. A current binary replaces the dataset on its own, so the manual
delete is only needed if you are stuck on an older one:

```bash
./predictmarketing forecast CORRECTED.csv -series NAME -db /tmp/repair.db
# older binaries only:
# sqlite3 /tmp/repair.db "DELETE FROM series WHERE series_id='NAME'"
```

Nothing references `series`, so `runs` and `forecasts` survive untouched — which
is the point: the forecasts are the part you cannot rebuild. `import` here would
wipe them; that is the whole reason this step is a `forecast`. It does cost one
extra `runs` row, which §4 below removes if it matters. Re-check the orphan
query, `PRAGMA integrity_check` and `PRAGMA foreign_key_check` afterwards.

---

## 4. `accuracy` reports more days than were ever forecast

Nothing dedups a run. `saveRun` always inserts a fresh id, `input_sha256` is
recorded and never read back, and `accuracy`'s `days` column is a row count. So
forecasting the same file with the same model twice stores the whole thing twice
and doubles the sample.

**Two things narrow when this can happen now.** `import` empties the database
first, so repeated imports no longer pile up (`AGENTS.md` §2c) — a doubled sample
comes from repeated `forecast` runs, or from a file written before that change.
And `import` stores the window in the model name (`chronos2@90d`, not
`chronos2`), so the six window runs of one import are distinct by construction
and are not what this query is looking for.

**The query groups by `model`, so it cannot see across that naming.** A
`chronos2@full` from `import` and a `chronos2` from `forecast` over the same file
are two rows in `accuracy` however identical their `input_sha256`. That is a gap
in the detection, not in the data: decide by hand which one you meant to keep.

Detect:

```bash
sqlite3 /tmp/repair.db "
SELECT series_id, model, as_of, substr(input_sha256,1,12) AS input, COUNT(*) AS n
FROM runs GROUP BY series_id, model, as_of, input_sha256 HAVING n > 1;"
```

Repair. **Forecasts first** — `foreign_keys` is on and there is no
`ON DELETE CASCADE`, so deleting the run first is refused with
`FOREIGN KEY constraint failed`:

```bash
sqlite3 /tmp/repair.db "PRAGMA foreign_keys=ON;
CREATE TEMP TABLE doomed AS
  SELECT id FROM runs WHERE rowid NOT IN (
    SELECT MIN(rowid) FROM runs
    GROUP BY series_id, model, as_of, input_sha256);
SELECT 'deleting', COUNT(*) FROM doomed;
DELETE FROM forecasts WHERE run_id IN (SELECT id FROM doomed);
DELETE FROM runs      WHERE id     IN (SELECT id FROM doomed);
PRAGMA foreign_key_check;"
```

Keep the oldest by `rowid`, never by `id`: run ids are random hex, and they are
not even a consistent width — `forecast` and `import` use two different
generators.

Then confirm the day counts came back down:

```bash
./predictmarketing accuracy -db /tmp/repair.db
```

---

## 5. Putting it back

Nothing above is done until the repaired copy has been read by the tool itself,
not just by `sqlite3`:

```bash
sqlite3 /tmp/repair.db "PRAGMA integrity_check; PRAGMA foreign_key_check;"
./predictmarketing accuracy -db /tmp/repair.db
./predictmarketing accuracy -db /tmp/repair.db -by-day
```

Then swap it in. State what you deleted and how many rows — a repair that removed
more than it said is worse than the fault.

## What this skill will not do

- **Recover a forecast that was deleted.** Re-running the model today produces a
  forecast made with today's data, which is a different thing and must not be
  presented as the old one.
- **Migrate a table.** `AGENTS.md` §4a is explicit that there are no table
  migrations; the escape is a new file and a re-import.
- **Substitute for a backup.** There are none, deliberately — the CSV exports are
  the thing worth keeping (`sqlite-optimization`, project notes). If the exports
  are gone too, `raw` is the last copy of them and must not be deleted to fix
  anything. Note that the exports rebuild `raw` and `series` only: since `import`
  wipes, a `cp pm.db` taken **before** an import is the only thing that brings
  back `runs` and `forecasts`.
