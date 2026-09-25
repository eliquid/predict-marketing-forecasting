---
name: new-export
description: Import a fresh ad-platform export, forecast it, and score the forecasts made previously. Use for the recurring weekly or daily run on a new Google Ads CSV.
---

# Importing a new export

## Get the right download first

Google Ads, campaigns view: **download → More options**, segmented **daily**,
format **`.csv`** — **not `.csv (Excel)`**. The Excel one is UTF-16 and
tab-separated despite its name, and is refused. `AGENTS.md` §2a0 has the
conversion command if a file has already been downloaded the wrong way.

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

A window longer than the file is **skipped and announced**, not an error, so a
short export still produces everything it can. On a file of exactly 90 days the
90d window is skipped as a duplicate of the whole file and the average falls back
to `full`, which is the last 90 days there.

**Why only one line.** A walk-forward backtest over 31 daily origins had the
90-day window beating the whole file and 270 days at all 16 horizons, and the
average beating both individual models at account level. The other five runs are
in the database for `accuracy` to score — they are just not what the report
recommends. See `AGENTS.md` §2c.

At least 90 days is required; 365 is better, 730 best. All three windows need
**271** days.

The steps below are the manual equivalent, for when you want one model, one
metric, or a horizon the import does not use.

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

This is the recurring job: a newer CSV arrives, and it does two things at once —
it supplies the actuals for forecasts already stored, and it is the basis for the
next forecast.

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

2. **Read what it says it did.** Every line matters:

   ```
   5 rows per day, split by "Campaign" (750 rows kept in the raw table)
   forecasting: Cost, Impr., Clicks
   for 5: (account), Brand Search, Shopping - All, ...
   switched off in the export, stored but not forecast: Video Awareness, ...
   not forecast, look like identifiers: Campaign ID
   stored, not forecast (you set these, you do not predict them): Budget
   stored but not numbers: Campaign status, Campaign, ...
   ```

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
   awk -F, 'NR>1 && $1==d {print $3": "$2}' d="$(awk -F, 'NR>1{print $1}' \
       "Campaign report.csv" | sort | tail -1)" "Campaign report.csv" | sort
   ```

   Its history is still queryable — `SELECT ... FROM series WHERE entity=...`
   returns every day of it. Only the forecast is absent, and only because
   nothing can be forecast about a campaign that is switched off.

3. **Score the previous forecasts**, now that their days have actuals:

   ```bash
   ./predictmarketing accuracy -db ads.db
   ./predictmarketing accuracy -db ads.db -metric Cost -by-day
   ```

   `in range` should sit near 80%: that is how often the actual landed inside the
   q10–q90 band. Much below means the bands are too narrow to trust.

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
