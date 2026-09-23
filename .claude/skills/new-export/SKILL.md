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

That is the whole job: it forecasts with every model, writes report 1
(`chronos2` + `timesfm3`) immediately, files the CSV into `data/imported/`, then
trains `chronos2ft` and writes report 2 with all three. At least 90 days of
history is required; 365 is better, 730 best. See `AGENTS.md` §2c.

The steps below are the manual equivalent, for when you want one model, one
metric, or a horizon the import does not use.

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
   no activity at all, not forecast: Video Awareness, ...
   not forecast, look like identifiers: Campaign ID
   stored, not forecast (you set these, you do not predict them): Budget
   stored but not numbers: Campaign status, Campaign, ...
   ```

   If a column you expected to be forecast is in one of the "not forecast"
   lines, that is the classification rule (`AGENTS.md` §4b) — check the name
   before assuming a bug.

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
| `a Campaign is called "(account)"` | name clash with the total; use `-by "Campaign ID"` |
| `N days is too few` | fewer than 32 days of history |

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

Each run leaves a report next to the CSV and appends to `pm.db`. Both are
gitignored, and `pm.db` is what `accuracy` scores against, so keep it. Clear only
the reports when they pile up:

```bash
find . -name '*_forecast_*.html' -not -path './models/*' -delete
```
