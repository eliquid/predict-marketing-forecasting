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

## Train

```bash
models/.venv/bin/python models/finetune.py "Campaign report.csv" --steps 2000
```

`--steps` is the whole instruction and it runs all of them. **There is no time
limit, and there must not be one.** A clock that ended training early left an
adapter that was undertrained but looked finished everywhere it was used -- the
registry said `steps: 1210`, and nothing else did. How long it takes is a
property of how much data you have; on a two-year, fourteen-campaign export it
was about 17 minutes.

Writes `models/finetuned/chronos2ft/` (4.9 MB) and `models/finetuned.json`.

Then it is just another model:

```bash
./predictmarketing forecast "Campaign report.csv" -model chronos2ft
```

## Before you believe it helped

**Score it against the stock model on days it never saw.** This is the whole
point and it is easy to skip:

```bash
./predictmarketing accuracy -db pm.db
```

`chronos2ft` will be **absent from that table** if every forecast it has made
falls inside its training window — that is correct, not a bug. To score it you
need a forecast whose days are *after* `trained_through`, which means either
waiting for real days to arrive, or retraining on a cutoff and forecasting past it.

Never write an accuracy query without `trained_on = 0`.

## What was already measured, so you need not redo it

Full fine-tuning is feasible on an M1 Pro with 32 GB — LoRA is only 19% faster and uses
the same peak memory. LoRA was chosen for the 99x smaller checkpoint.

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

The adapter config holds an absolute path to the base weights. The worker rewrites
it on load if the project has moved, so that alone never needs fixing by hand.
