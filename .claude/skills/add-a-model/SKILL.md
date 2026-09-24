---
name: add-a-model
description: Wire another forecasting model into Predict Marketing. Use when adding any new time-series model alongside timesfm3, chronos2 and chronos2ft, or when a model's worker needs rewriting.
---

# Adding a model

Read `AGENTS.md` §4 first — it defines the protocol this depends on.

The design claim being tested is: **a new model costs one Python file and one
line of Go.** That line makes it available to `forecast -model NAME`, to
`models`, and to `report`, which redraws whatever runs are stored. If you find
yourself editing a second Go file to make the *forecast* work, stop; something
is wrong with the change, not with the rule.

The one deliberate exception is the recurring job: `import` runs a hard-coded
list (`[]string{"chronos2", "timesfm3"}` for report 1, then `chronos2ft`) rather
than everything in the `models` map, because report 2 exists only for the model
that has to be trained first. A new pretrained model that should run on every
import needs its name in that list in `import.go` too.

## Steps

1. **Add it to `models/requirements.txt`** if it needs new Python packages. Pin
   the version. Run:
   ```bash
   VIRTUAL_ENV=$PWD/models/.venv uv pip install -r models/requirements.txt
   ```

2. **Add the weights to `models/fetch.py`** — repo id and a pinned revision, in
   the `MODELS` map, and its expected sha256 in `EXPECTED`. Then
   `./predictmarketing setup` to download and checksum them.

   `setup` will print a Hugging Face **"unauthenticated requests"** warning. That
   is expected for a public model and is not a failure — see `AGENTS.md` §2. Do
   not add token handling to `models/fetch.py`: `huggingface_hub` already reads
   `HF_TOKEN` from the environment. If the new model is **gated**, that is
   different and worth saying out loud in the README, because every user will
   then need their own Hugging Face account and token.

3. **Copy a worker.** `models/chronos2_worker.py` if the model takes covariates,
   `models/timesfm3_worker.py` if it does not. Name it `<name>_worker.py`.
   **Never name it after a Python package** — a script's own directory goes first
   on `sys.path` and will shadow the real package.

   Keep these parts exactly as they are:
   - the `_OUT` / `sys.stdout = sys.stderr` block (stdout belongs to the protocol)
   - the `load_verified()` call (recomputes the weights sha256 before use).
     `models/chronos2ft_worker.py` is the one shipped exception: it verifies its
     adapter instead and leaves the base weights unchecked (`AGENTS.md` §4c).
     Copy that pattern only for a model that is a *derivative* of one already in
     `models/weights.json`, and say in the handshake which hash you are reporting.
   - the `reply({...})` handshake, declaring `covariates` honestly

   Change only: which library is imported, how the model is loaded, and the one
   call that turns a `(metrics, days)` matrix into `(metrics, horizon, quantiles)`.

   **The handshake's quantiles must include 0.1 and 0.9 as decimal literals, in
   ascending order.** `forecast_accuracy` joins `low` and `high` on exactly those
   two values, so a model with any other band reports `in range 0%` in
   `accuracy` — a number that reads as a catastrophically bad model rather than a
   missing column. Ascending matters separately: `checkForecast` sorts each day's
   values and `saveRun` then labels position *j* with `quantiles[j]`, so a
   declared order of `[0.1, 0.2, 0.9]` answered out of order is stored under the
   wrong labels and reported as an ordinary small crossing. Nothing validates
   either property. Check both right after the first stored run:

   ```bash
   sqlite3 pm.db "SELECT COUNT(*), SUM(inside_range IS NULL)
                  FROM forecast_accuracy WHERE model='yourmodel' AND actual IS NOT NULL;"
   ```

   The second number must be 0. Note `np.arange(0.1, 1.0, 0.1)` gives exact 0.1
   and 0.9 but a 0.3 of `0.30000000000000004` — use stored literals, not `arange`.

4. **Add one line** to the `models` map in `worker.go`:
   ```go
   "yourmodel": "models/yourmodel_worker.py",
   ```

   That map governs `forecast`, `models` and `report`. It does **not** govern
   `import`, which names its models directly (`import.go` runs `chronos2` and
   `timesfm3` for report 1, then `chronos2ft` for report 2). A new pretrained
   model added only to the map will never run on the recurring job.

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
`models/fetch.py`/`requirements.txt` — plus the `import.go` model list if the
model is meant to run on every import. **Any other changed Go file means the
abstraction leaked** — find out why instead of accepting it.

## Then prove the model is real

Run the checks in `.claude/skills/verify`. In particular, a new model must pass
`TestEachMetricGetsItsOwnForecast`, which proves it forecasts every metric
genuinely rather than deriving some from others.

## Paths in a new worker

Anchor everything to the script's own location:

```python
HERE = os.path.dirname(os.path.abspath(__file__))
```

Never open a path relative to the current directory. A worker is started by the
Go program, which may be run from anywhere, and a cwd-relative path would look
fine in testing and fail the moment someone runs the tool from another folder.
Every existing worker does this, and a test enforces it.
