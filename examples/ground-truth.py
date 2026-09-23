"""Prove the pipeline does not alter forecasts.

Rebuilds each entity's input matrix from the CSV, calls the model's own library
directly, and compares with what the tool stored. The only difference permitted
is sorting, and only where the model's own quantiles crossed.

    ./predictmarketing forecast FILE.csv -model chronos2 -db /tmp/gt.db
    ./predictmarketing forecast FILE.csv -model timesfm3 -db /tmp/gt.db
    models/.venv/bin/python examples/ground-truth.py /tmp/gt.db FILE.csv

Every line must say EXACT.

NOTE: sums are accumulated in float64 and cast to float32 once at the end, which
is what the Go side does. Accumulating in float32 instead rounds at every step
and produces a matrix that differs in the fourth decimal -- an earlier version of
this script did exactly that and reported a phantom mismatch on the account total.
"""
import collections, csv, json, os, sqlite3, sys
import numpy as np

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
os.environ["HF_HOME"] = os.path.join(HERE, "models", "cache")
os.environ["HF_HUB_OFFLINE"] = "1"

DB  = sys.argv[1] if len(sys.argv) > 1 else "/tmp/gt.db"
CSV = sys.argv[2] if len(sys.argv) > 2 else os.path.join(HERE, "examples/02-marketing.csv")
Q   = [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9]
ACCOUNT = "(account)"

meta = json.load(open(os.path.join(HERE, "models", "weights.json")))
db = sqlite3.connect(DB)

rows = list(csv.reader(open(CSV)))
hdr, data = rows[0], rows[1:]
ix = {n: i for i, n in enumerate(hdr)}
days = sorted({r[0] for r in data})
di = {d: i for i, d in enumerate(days)}


def number(s):
    return float(s.strip().replace(",", "").replace("$", "").replace("£", "").replace("€", ""))


def check_columns(metrics, group_by):
    """The database records which columns were forecast. If this CSV does not
    have them, it is the wrong file -- say so, rather than dying on a KeyError
    forty lines later."""
    missing = [n for n in list(metrics) + ([group_by] if group_by else []) if n not in ix]
    if missing:
        sys.exit(
            f"{CSV} has no column named {', '.join(repr(m) for m in missing)}.\n"
            f"That run forecast: {', '.join(metrics)}\n"
            f"This file has   : {', '.join(hdr)}\n"
            f"Pass the CSV the forecast was made from:\n"
            f"  python examples/ground-truth.py {DB} path/to/that.csv")


def matrix(entity, metrics, group_by):
    m = np.zeros((len(metrics), len(days)), dtype=np.float64)   # see NOTE above
    for r in data:
        if entity != ACCOUNT and group_by and r[ix[group_by]] != entity:
            continue
        for k, name in enumerate(metrics):
            m[k][di[r[0]]] += number(r[ix[name]])
    return m.astype(np.float32)


def latest(model):
    row = db.execute("SELECT id, metrics, entities, group_by, horizon FROM runs "
                     "WHERE model=? ORDER BY created_at DESC LIMIT 1", (model,)).fetchone()
    if row is None:
        return None
    rid, metrics, entities, group_by, horizon = row
    out = collections.defaultdict(lambda: collections.defaultdict(dict))
    for entity, metric, day, v in db.execute(
            "SELECT entity, metric, day, value FROM forecasts WHERE run_id=? "
            "ORDER BY entity, metric, day, quantile", (rid,)):
        out[entity][metric].setdefault(day, []).append(v)
    got = {e: {m: np.array([d[k] for k in sorted(d)]) for m, d in ms.items()}
           for e, ms in out.items()}
    return metrics.split(","), entities.split("\x1f"), group_by, horizon, got


ok = True
for model in ("chronos2", "timesfm3"):
    info = latest(model)
    if info is None:
        print(f"  {model}: no run found in {DB} -- forecast with it first")
        continue
    metrics, entities, group_by, horizon, got = info
    check_columns(metrics, group_by)

    if model == "chronos2":
        from chronos import Chronos2Pipeline
        p = Chronos2Pipeline.from_pretrained(meta["chronos2"]["path"], device_map="cpu")
        run = lambda ctx: np.asarray(p.predict_quantiles(
            [{"target": ctx}], prediction_length=horizon, quantile_levels=Q)[0][0], dtype=float)
    else:
        import timesfm
        f = timesfm.TimesFM3Forecaster.from_pretrained(meta["timesfm3"]["path"], device="cpu")
        run = lambda ctx: np.asarray(f.predict(
            context=ctx, horizon=horizon, return_quantiles=True).quantiles, dtype=float)

    for entity in entities:
        direct = run(matrix(entity, metrics, group_by))
        worst = max(float(np.abs(np.sort(direct[k], axis=-1) - got[entity][m]).max())
                    for k, m in enumerate(metrics))
        print(f"  {model:9} {entity[:28]:30} max diff {worst:.3g}  "
              f"{'EXACT' if worst == 0 else 'DIFFERS'}")
        ok &= worst == 0

print("\nVERDICT:", "every entity's forecast is the model's own output, unaltered"
      if ok else "THE PIPELINE IS ALTERING FORECASTS")
sys.exit(0 if ok else 1)
