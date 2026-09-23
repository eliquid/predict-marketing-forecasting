---
name: add-a-model
description: Wire a third forecasting model into Predict Marketing. Use when adding any new time-series model alongside timesfm3 and chronos2, or when a model's worker needs rewriting.
---

# Adding a model

Read `AGENTS.md` §4 first — it defines the protocol this depends on.

The design claim being tested is: **a new model costs one Python file and one
line of Go.** If you find yourself editing a second Go file, stop; something is
wrong with the change, not with the rule.

## Steps

1. **Add it to `models/requirements.txt`** if it needs new Python packages. Pin
   the version. Run:
   ```bash
   VIRTUAL_ENV=$PWD/models/.venv uv pip install -r models/requirements.txt
   ```

2. **Add the weights to `models/fetch.py`** — repo id and a pinned revision, in
   the `MODELS` map. Then `./predictmarketing setup` to download and checksum them.

3. **Copy a worker.** `models/chronos2_worker.py` if the model takes covariates,
   `models/timesfm3_worker.py` if it does not. Name it `<name>_worker.py`.
   **Never name it after a Python package** — a script's own directory goes first
   on `sys.path` and will shadow the real package.

   Keep these parts exactly as they are:
   - the `_OUT` / `sys.stdout = sys.stderr` block (stdout belongs to the protocol)
   - the `load_verified()` call (recomputes the weights sha256 before use)
   - the `reply({...})` handshake, declaring `covariates` honestly

   Change only: which library is imported, how the model is loaded, and the one
   call that turns a `(metrics, days)` matrix into `(metrics, horizon, quantiles)`.

4. **Add one line** to the `models` map in `worker.go`:
   ```go
   "yourmodel": "models/yourmodel_worker.py",
   ```

5. **Verify.**
   ```bash
   ./predictmarketing models        # your model appears with its real capabilities
   ./predictmarketing forecast testdata/example.csv -model yourmodel -horizon 7
   go test ./...
   ```

## The acceptance test

```bash
git diff --stat
```

One new `.py` file, one changed line in `worker.go`, and entries in
`fetch.py`/`requirements.txt`. **Any other changed Go file means the abstraction
leaked** — find out why instead of accepting it.

## Then prove the model is real

Run the checks in `.claude/skills/verify`. In particular, a new model must pass
`TestEachMetricGetsItsOwnForecast`, which proves it forecasts every metric
genuinely rather than deriving some from others.
