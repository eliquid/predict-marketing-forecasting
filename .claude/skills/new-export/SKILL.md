---
name: new-export
description: Import a fresh ad-platform export and forecast it, or build a scoreable forecast history with the single-model command instead. Use for the recurring weekly or daily run on a new Google Ads CSV.
---

# Importing a new export

## Get the right download first

Google Ads, campaigns view: **download → More options**, segmented **daily**,
date range **ending yesterday**, format **`.csv`** — **not `.csv (Excel)`**. The
Excel one is UTF-16 and tab-separated despite its name, and is refused.
`AGENTS.md` §2a0 has the conversion command if a file has already been
downloaded the wrong way.

**End the range on the last full day of spend — never today.** A day still
running holds the spend so far, not the spend it will end with, and nothing in
this tool can tell that from a genuine collapse. Both models lean on the newest
days, so one partial day at the end drags the whole forecast down: measured, the
same day read 4,435.52 taken mid-afternoon and 6,378.35 once complete, and
forecasting from the partial one came out **28% low on both models**.

Check before importing — this is not something the tool does for you:

```bash
awk -F, 'NR==1{for(i=1;i<=NF;i++) if($i=="Cost") c=i; next}
         c{t[$1]+=$c} END{for (d in t) print d, t[d]}' "Campaign report.csv" \
  | sort | tail -8
```

Find the column by its **header**, never by a fixed position: exports differ in
how many columns they carry, and a hardcoded field number on the wrong file adds
up a column of empty strings and prints a tidy `0` for every day — which is the
one output this check cannot tell from a real collapse.

A last day far below the ones before it means the export ran too early.
Re-download it ending on yesterday.

## The short version

```bash
cp "Campaign report.csv" data/
./predictmarketing import
```

Run the binary from anywhere: `data/` and `pm.db` are anchored to the
installation, not to the shell, so there is one folder and one database no matter
where you stand. The `data/` the CSV goes into is the **installation's** one for
the same reason — copy it somewhere else and `import` will say, with the full
path, that it found no files.

**`import` empties the database first.** It is the command for bringing in an
account, so everything already stored — forecasts, runs, history — is deleted
before the new file is read. The wipe happens after every model has answered, so a refused forecast — or a bad
export cannot destroy your data and give nothing back, and only the first file
of a batch wipes. If you need a scoreable forecast history, use `forecast` with
an explicit `-db`, which does not wipe (`AGENTS.md` §4a).

That is the whole job. It forecasts the file **three times** — whole file, last
270 days, last 90 days — with both pretrained models and **stores all six runs**,
then writes report 1 showing the actuals and **one forecast line**: `average@90d`,
the mean of the two 90-day runs. It files the CSV into `data/imported/`, trains
`chronos2ft` on the **whole file**, and writes report 2 with that line added.
Reports go to `data/reports/`.

```
  full window (400 days)
  270d window (270 days)
  90d window (90 days)
  average@90d: the mean of chronos2@90d and timesfm3@90d
```

**Read the `forecasting:` line every time.** It names the columns `import` is
modelling *and the concept each one matched* — `Cost (spend), Impr.
(impressions), Clicks` — so the allow-list shows its working, not just its
verdict (`AGENTS.md` §4b). A numeric column that matched nothing is named on the
next line, `numeric, but not a metric this forecasts:`, stored and never
forecast. A column demoted to **text** because a few cells stopped parsing is
named nowhere: `import` still prints nothing about text columns, identifiers or
settings. It just quietly leaves the `forecasting:` list.

A window longer than the file is **skipped and announced**, not an error, so a
short export still produces everything it can. On a file of exactly 90 days the
90d window is skipped as a duplicate of the whole file and the average falls back
to `full`, which is the last 90 days there.

**Why only one line.** A walk-forward backtest over 31 daily origins had the
90-day window beating the whole file and 270 days at every horizon and at both
levels, and the average beating both individual models at account level
(17.33% MAPE). The other five runs are in the database for `accuracy` to score —
they are just not what the report recommends. See `AGENTS.md` §2c.

At least 90 days is required; 365 is better, 730 best. All three windows need
**271** days.

The steps below are **not** what `import` does. They are the single-model command
underneath it, for when you want one model, one metric, or a horizon the import
does not use — and, because they leave the database alone, they are the only
route to a forecast history `accuracy` can score.

## If you only want the reports back

```bash
./predictmarketing report
```

That redraws `data/reports/` from what is already in `pm.db` — no CSV is read,
nothing is forecast and nothing is retrained. Reach for it when a report has been
deleted or the drawing has changed, and keep `import` for a new export. A rate
comes back without its `%` sign; re-importing is what restores that. See
`AGENTS.md` §2c.

Read `AGENTS.md` §2a for how a campaign export is read, and §4a for what the
database holds.

These steps use `forecast`, not `import`, and that is the whole reason they are
here: `forecast` **does not empty the database**. A newer CSV run through it does
two things at once — it supplies the actuals for the forecasts already stored,
and it is the basis for the next one. Put them in a database of your own with
`-db` and the history accumulates, which is what makes step 3 possible at all.
Run `import` against that same file and both halves are lost: the wipe deletes
the forecast you were about to score before it stores the actuals that would
score it.

## Steps

1. **Use the same `-series` name as last time.** That is what ties the new
   actuals to the old forecasts. A different name starts a separate dataset and
   nothing gets scored.

   ```bash
   for m in chronos2 timesfm3 chronos2ft; do
     ./predictmarketing forecast "Campaign report.csv" \
         -model $m -horizon 7 -history 45 -series "Google Ads" -db ads.db
   done
   ```

   Drop `chronos2ft` if no adapter has been trained; see the `finetune` skill.
   Retraining it on the newer export is optional and separate.

   If it refuses with **uneven rows per day**, the export lists each campaign
   only on the days it ran rather than zero-filling them. Add `-fill-absent`
   (to `import` or to `forecast`) and run it again. It is safe on a complete
   export too — it changes nothing there. `AGENTS.md` §2a2 has the whole rule;
   the two parts worth knowing at the prompt are that a day missing from the
   *middle* of a campaign's run is still refused as a broken download, and that
   a campaign the export stops listing is treated as stopped: stored in full,
   named on screen, not forecast.

2. **Read what it says it did.** Every line matters:

   ```
   5 rows per day, split by "Campaign" (750 rows kept in the raw table)
   forecasting: Cost, Impr., Clicks
   for 5: (account), Brand Search, Shopping - All, ...
   switched off in the export, stored but not forecast: Video Awareness, ...
   stopped running before the export's last day, stored but not forecast: ...
   no activity at all, stored but not forecast: ...
   note: the (account) series includes those campaigns' history, so its forecast assumes they keep spending. Per-campaign figures do not.
   renamed during this period, kept as one series: ...
   numeric, but not a metric this forecasts: Quality score
   not forecast, look like identifiers: Campaign ID
   stored, not forecast (you set these, you do not predict them): Budget
   currency: USD (every money figure below is in it)
   account figure is the blended rate, weighted by the column named: CTR (by Impr.)
   stored but not numbers: Campaign status, Campaign, ...
   ```

   Each line appears only when the file gives it a reason to. Two are easy to
   miss. The **blend line** says whether an account rate was weighted by a real
   denominator column or is a plain mean of the campaigns that reported — the two
   answers differ by multiples and used to print identically. **`currency:`**
   appears only when the export carries a currency column; a file mixing two is
   refused outright.

   If a column you expected to be forecast is in one of the "not forecast"
   lines, that is the classification rule (`AGENTS.md` §4b) — check the name
   before assuming a bug.

   **A missing campaign is not a missing campaign.** `switched off in the
   export` means the CSV's campaign-status column says it is paused as of the
   file's last day, so it is stored in full and skipped by every model
   (`AGENTS.md` §2a1). It will not be in the report's dropdown either, because
   that lists what was forecast. Check its status in the export before treating
   it as a bug:

   ```bash
   awk -F, 'NR==1{for(i=1;i<=NF;i++){if($i=="Campaign")n=i; if($i=="Campaign status")s=i}; next}
            $1>d{d=$1; delete st} $1==d{st[$n]=$s}
            END{print "as of "d; for (k in st) print "  "k": "st[k]}' "Campaign report.csv"
   ```

   Headers again, not field numbers, and only the file's **last day** — that is
   the day the rule is applied on (`AGENTS.md` §2a1).

   Its history is still queryable — `SELECT ... FROM series WHERE entity=...`
   returns every day of it. Only the forecast is absent, and only because
   nothing can be forecast about a campaign that is switched off.

3. **Score the previous forecasts**, now that their days have actuals.

   **This only works for the `forecast` steps above, not for `import`.**
   `import` empties the whole database before it stores anything (`AGENTS.md`
   §4a), so a forecast made by one import is gone by the next — deleted by the
   very import that brings the actuals to score it against. The steps in this
   section use `forecast` with an explicit `-db`, which does not wipe, and that
   is what makes the history below accumulate.

   ```bash
   ./predictmarketing accuracy -db ads.db
   ./predictmarketing accuracy -db ads.db -metric Cost -by-day
   ```

   `in range` should sit near 80%: that is how often the actual landed inside the
   q10–q90 band. Much below means the bands are too narrow to trust.

   **Read `days` before you read anything else.** It is a count of scored rows,
   not a date range, and nothing deduplicates runs, so the same forecast made
   twice counts twice. One 7-day backtest is seven observations, and `-by-day`
   splits those into seven rows of **one** — every cell then prints 0% or 100%,
   which reads like a finding and is a coin flip. Below roughly ten observations
   per row, read `avg error` and ignore `in range` (`AGENTS.md` §4a).

4. **Sanity-check the aggregation.** Campaign forecasts are produced
   independently of the account, so they will not sum exactly — a few percent is
   normal, a large gap is not:

   ```bash
   sqlite3 ads.db "
   SELECT metric,
          SUM(CASE WHEN entity='(account)' THEN value END)  AS account,
          SUM(CASE WHEN entity<>'(account)' THEN value END) AS campaigns
   FROM forecasts WHERE run_id=(SELECT id FROM runs ORDER BY created_at DESC LIMIT 1)
     AND quantile=0.5 GROUP BY metric;"
   ```

## If the import is refused

| Message | What it means |
|---|---|
| `N day(s) missing between X and Y` | the export has a gap; re-export a complete range |
| `X has N rows but Y has M` | a campaign is missing from some days; adding them up would invent a step |
| `several columns could separate them` | pick one with `-by "Campaign"` |
| `hold measured numbers rather than names` | the only column that fits is a metric (`Cost`). The export has no usable campaign column — re-export with `Campaign` or `Campaign ID` in it |
| `a Campaign is called "(account)"` | name clash with the total; use `-by "Campaign ID"` |
| `only N days of history, and at least 90 are needed` | `import`'s own gate, hit before any of the above; the CSV is left in `data/` |
| `N days is too few` | fewer than 32 days of history — the floor `forecast` refuses at |

None of these are worth working around by editing the CSV's numbers. Fix the
export.

## Ad-hoc questions afterwards

Everything from the original file is in `raw`, verbatim:

```sql
SELECT day, json_extract(data,'$.Campaign'), json_extract(data,'$.Cost')
FROM raw
WHERE source='Google Ads' AND json_extract(data,'$."Campaign status"')='Enabled'
ORDER BY day DESC LIMIT 20;
```

## Afterwards

A `forecast` run leaves a report beside the CSV and appends to whatever `-db`
named (`ads.db` in the steps above, otherwise `pm.db`). An `import` run puts its
two reports in `data/reports/` instead. Reports and databases are all gitignored,
and the database is what `accuracy` scores against, so keep it. Clear only the
reports when they pile up:

```bash
find . -name '*_forecast_*.html' -not -path './models/*' -delete   # forecast's
find data/reports -name '*.html' -delete                           # import's
```

`find`, not `rm *.html`: zsh fails the whole command when a glob matches nothing.

`report` rewrites the second set from the database whenever you want them back.
