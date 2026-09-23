# Examples

Four files covering the shapes of data this tool sees, and two scripts that run
for real. An agent should prefer running these over trusting a description.

| File | What it shows |
|---|---|
| `01-simple.csv` | the minimum: `date,spend` |
| `02-marketing.csv` | the normal case: spend, impressions, clicks, conversions, revenue |
| `03-platform-export.csv` | a real export: a text column, awkward header names |
| `04-with-budget.csv` | a planned column you can tell the model about in advance |

## Run everything

```bash
./examples/walkthrough.sh
```

Runs the eight things people actually do — one metric, many metrics, a messy
export, a column subset, known-future values, the refusal when a model cannot use
them, what each model is, and the run history.

## Prove the numbers are the models'

```bash
./predictmarketing forecast examples/02-marketing.csv -model chronos2 -db /tmp/gt.db
./predictmarketing forecast examples/02-marketing.csv -model timesfm3 -db /tmp/gt.db
models/.venv/bin/python examples/ground-truth.py /tmp/gt.db
```

Calls each library directly on the same matrix and compares with what was stored.
**Every line must say `EXACT`.** The only permitted difference is sorting, and
only where the model's own quantiles crossed.

Run this after any change between the CSV and the database.

## Clean up

```bash
rm -f examples/*_forecast_*.html examples/walkthrough.db*
```
