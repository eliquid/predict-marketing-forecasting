---
name: finetune
description: Train or retrain the chronos2ft LoRA adapter on the user's own data. Use when fine-tuning Chronos-2, refreshing the adapter with newer data, or deciding between LoRA and full fine-tuning.
---

# Fine-tuning Chronos-2

Read `AGENTS.md` §4c first — it holds the measured numbers and the leakage rule.

The base Chronos-2 weights must already be installed (`./install.sh`). If you are
re-running `setup` first, the Hugging Face "unauthenticated requests" warning it
prints is expected and harmless — `AGENTS.md` §2 explains it.

`predictmarketing import` now trains the adapter for you on every import and
writes a second report with it included (`AGENTS.md` §2c). Use the steps below
when you want to train on a specific file, with different settings, or without
running a whole import.

Three things about the import path are worth knowing before you go looking for
the adapter's numbers:

- **It is given the whole file**, never one of the shortened windows the two
  pretrained models also run over. It is the one model that learns from the data
  rather than reading it, so more of it is what training has to work with.
- **Its run is stored as `chronos2ft@full`**, not `chronos2ft`. `accuracy`
  groups by that column, so a query written against the bare name finds nothing.
- **It is left out of `average@90d`**, the line report 1 draws. It is fitted to
  the same data it would be averaged into and has not beaten the stock models
  (`AGENTS.md` §4c), so a weaker, leakier opinion is kept out of the ensemble.
  Report 2 is where it appears, as a line of its own.

`import` passes the trainer the metric and group columns **of the file it just
read** — `--metrics`, `--group`, and `--label` when the group column is an ID —
so it fits the same columns and the same campaigns the forecaster ran, and its
skipped list prints campaign names rather than bare IDs.
The trainer's own defaults (`--metrics Cost,Impr.,Clicks --group Campaign`) are
Google-Ads-shaped and apply only when you run it **by hand** — pass the flags
yourself then, or on any other export it either exits with
`columns not in <file>: ['Impr.']` or, with no matching group column, **trains
happily on one series**, everything collapsed into `(account)`. `train_series: 1`
in `models/finetuned.json` is the only record that happened; read that field
before believing a fine-tune covered the campaigns (`AGENTS.md` §4c).

## Train

```bash
models/.venv/bin/python models/finetune.py "Campaign report.csv" --steps 2000
```

`--steps` is the whole instruction and it runs all of them. **There is no time
limit, and there must not be one.** A clock that ended training early left an
adapter that was undertrained but looked finished everywhere it was used -- the
registry said `steps: 1210`, and nothing else did. How long it takes depends on
both the step count and the file — `AGENTS.md` §2c has the two runs that were
actually measured. Read `train_seconds` out of `models/finetuned.json` after a
run rather than quoting a figure at anyone.

**It never stops, shortens, or warns about the data.** Every step runs. Its only
two exits are a missing column (before training) and a missing adapter (after
it) — see `AGENTS.md` §4c, which also records why the old `NOTE:` about small
datasets was removed rather than reworded. If you think the adapter is not
helping, measure it below; do not make the trainer say so.

It prints which campaigns it is **not** training on before it starts:

```
not training on 9 switched-off, stopped or never-active campaign(s): ...
training on 7 series x 1099 days x 3 metrics
data runs <first day> .. <last day>
```

`load_series` skips exactly what the forecaster skips, and that is **three**
rules, not one: switched off as of the export's last day, never moved, and
*stopped* — a campaign whose rows end before the file's last day (`AGENTS.md`
§2a1/§2a2). Miss the third and the trainer fits dead tails the forecaster will
never be asked about. All three are still stored in full; they are left out of
training and forecasting only.
Training on a campaign the forecaster then refuses to run spends steps fitting
series nobody will ever see. `running_groups` in `models/finetune.py` and
`runningEntities` in `ingest.go` implement the same rule from the same column,
and `CAMPAIGN_STATES` / `campaignStates` are the lists that have to stay in
step. `python3 models/test_finetune.py` checks the Python half on its own, with
no venv needed; `go test -run TestFinetuneAgreesOnWhatIsRunning` runs it too.

Writes `models/finetuned/chronos2ft/` (4.9 MB) and `models/finetuned.json`.

Before it forecasts anything it says whose numbers it learned from:

```
note: chronos2ft is fitted to <export>.csv, through <trained_through>. It is the
wrong model for anyone else's numbers.
```

The worker sends `trained_on` — the **basename** of `source_csv`, never the full
path, because the report is shareable. Before this, forecasting a second account
with the first one's adapter looked identical to forecasting the account it was
trained on.

Then it is just another model:

```bash
./predictmarketing forecast "Campaign report.csv" -model chronos2ft
```

## Before you believe it helped

**Score it against the stock model on days it never saw.** This is the whole
point and it is easy to skip.

**Do not expect `pm.db` to hold the evidence.** `import` empties the database
before every import (`AGENTS.md` §4a), so the adapter's forecast is deleted by
the very import that would bring the actuals to judge it against — `accuracy`
cannot measure anything across imports. `forecast` does not wipe, so build the
comparison yourself in a database of your own:

```bash
rm -f /tmp/ft.db
for m in chronos2 chronos2ft; do
  ./predictmarketing forecast OLDER.csv -model $m -series X -db /tmp/ft.db -out /tmp/ft_$m.html
done
# ...once the days it forecast have actually happened:
./predictmarketing forecast NEWER.csv -model chronos2 -series X -db /tmp/ft.db -out /tmp/ft_a.html
./predictmarketing accuracy -db /tmp/ft.db
```

The second file only has to supply the actuals; `series` is replaced per dataset,
so use the **same `-series` name** or the join finds nothing.

`chronos2ft` will be **absent from that table** if every forecast it has made
falls inside its training window — that is correct, not a bug. To score it you
need a forecast whose days are *after* `trained_through`, which means either
waiting for real days to arrive, or retraining on a cutoff and forecasting past it.

One absence is not: if `trained_through` in `models/finetuned.json` is not
`YYYY-MM-DD`, the export was not ISO-dated, the guard compares a mismatched pair
of strings and marks **every** row trained-on — which looks identical to the
honest absence above (`AGENTS.md` §4c). Check that field before believing either
an absence or a score.

Never write an accuracy query without `trained_on = 0`.

## What was already measured, so you need not redo it

Full fine-tuning is feasible on an M1 Pro with 32 GB — LoRA is only 19% faster and uses
the same peak memory. LoRA was chosen for the roughly 90x smaller checkpoint.

Fine-tuning on 7 series made the forecast **worse** (34.5% MAPE vs 32.7% stock).
If you are asked to improve on that, the lever is **more series**, not more steps
or a different learning rate: more accounts, more platforms, or training per ad
group rather than per campaign.

Learning rates matter and differ by mode: `1e-6` for full, `1e-4` for LoRA. An
early run used `1e-5` for both and unfairly penalised full fine-tuning.

## Retraining

Just run it again — `models/finetuned.json` is overwritten with the new adapter's
checksum and cutoff. Old runs in the database keep the old checksum in their
`model_info`, so past forecasts still say truthfully what produced them.

If you want to keep the old adapter, copy `models/finetuned/chronos2ft/`
elsewhere first; nothing versions it for you.

## It is not shared

`share.sh` excludes the adapter and its registry. It was fitted to one account's
numbers, so it is the wrong model for anyone else's data even when it loads.
Recipients train their own from the same `models/finetune.py`.

## If it will not load

| Message | Cause |
|---|---|
| `no fine-tuned model yet` | nothing trained; run `models/finetune.py` |
| `the adapter on disk is not the one that was trained` | the file changed since training; retrain |
| `models/finetuned.json is unreadable` | registry corrupt; retrain |
| `points at ... which is outside models/` | the registry's `path` was made absolute or given a `../`. Only `adapter_model.safetensors` is checksummed, so an adapter outside `models/` could bring any `adapter_config.json` with it. Retrain, or move it back under `models/finetuned/` |

The adapter config holds an absolute path to the base weights. The worker rewrites
it on load if the project has moved, so that alone never needs fixing by hand.
