"""Fine-tune Chronos-2 on your own data with LoRA, and register the result.

    models/.venv/bin/python models/finetune.py "Campaign report.csv" [--steps N]

Produces models/finetuned/chronos2ft/ (about 5 MB) and records what it was
trained on in models/finetuned.json. The base weights are never touched.

LoRA rather than full fine-tuning, measured on this machine:
  full  2.5 steps/s, 3.3 GB peak, 456 MB checkpoint
  lora  3.0 steps/s, 3.3 GB peak, 4.9 MB checkpoint
Both are comfortable; LoRA is chosen for the roughly 90x smaller checkpoint, which makes
keeping a history of them practical.

IMPORTANT: the training cutoff is recorded as `trained_through`. Scoring the
fine-tuned model on days at or before that date measures memorisation, not
forecasting, and the accuracy view excludes them for exactly that reason.
"""
import argparse, csv, hashlib, json, os, sys, time
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
os.environ["HF_HOME"] = os.path.join(HERE, "cache")
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

OUT_DIR = os.path.join(HERE, "finetuned", "chronos2ft")
REGISTRY = os.path.join(HERE, "finetuned.json")
DEFAULT_METRICS = ["Cost", "Impr.", "Clicks"]


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


# The words an ad platform writes in its status column, and whether each one
# means the campaign is switched on. This has to agree with campaignStates in
# ingest.go: training on a campaign the forecaster then refuses to run is the
# worst of both, and the adapter would be fitted to series nobody ever sees.
CAMPAIGN_STATES = {
    "enabled": True, "active": True, "running": True, "live": True, "serving": True,
    "paused": False, "removed": False, "ended": False, "disabled": False,
    "archived": False, "stopped": False, "deleted": False, "draft": False,
}


def running_groups(hdr, data, group_col):
    """The campaigns the export says are switched on as of its last day, or None
    when the file carries no campaign-status column.

    The column is found by its values rather than its name. A Google Ads export
    calls it "Campaign status", but the same file carries a "Status" column of
    serving states ("Eligible (Limited)") and a "Status reasons" column of prose;
    matching on the word "status" picks the wrong one. A column is believed only
    when every value in it is a state in CAMPAIGN_STATES.
    """
    ix = {n: i for i, n in enumerate(hdr)}
    if group_col not in ix:
        return None
    col = None
    for i in range(len(hdr)):  # file order, so the leftmost one wins
        vals = {r[i].strip().lower() for r in data if i < len(r) and r[i].strip()}
        if vals and vals <= set(CAMPAIGN_STATES):
            col = i
            break
    if col is None:
        return None
    last = max(r[0] for r in data)
    return {r[ix[group_col]] for r in data
            if r[0] == last and CAMPAIGN_STATES.get(r[col].strip().lower(), False)}


# The same set as ingest.go's noData, and it has to stay the same set. A blank,
# a dash or a NaN is a cell the platform had no value for, which means 0.
NO_DATA = {"", "-", "--", "---", "\u2013", "\u2014",
           "n/a", "na", "nan", "null", "nil", "none"}


def num(s, where=""):
    """Parse one cell the way ingest.go's parseCell does, and refuse what it
    refuses.

    The two have to agree. Go strips currency symbols, thousands separators,
    percent signs and spaces, and reads a parenthesised value as negative; this
    handled only three of those, so `--`, `£10`, `(1,234.00)` and `1 234` — all
    of which the forecaster reads without complaint — killed the trainer with a
    bare ValueError traceback partway through a file.

    A blank, a dash or a NaN is not an error: the platform had no value for that
    cell, which means nothing happened, which means 0. NO_DATA lists them and Go's
    noData lists the same ones. Infinity is different -- a division that went
    wrong, not a measurement that is missing -- and both sides still refuse it.
    """
    raw = s.strip()
    neg = raw.startswith("(") and raw.endswith(")")
    t = raw.strip("()")
    for ch in ("$", "\u00a3", "\u20ac", ",", "%", " "):
        t = t.replace(ch, "")
    if t.lower() in NO_DATA:
        return 0.0  # no data is 0, the same as ingest.go's noData
    try:
        v = float(t)
    except ValueError:
        sys.exit(f"not a number: {raw!r}{where}")
    if v != v or v in (float("inf"), float("-inf")):
        sys.exit(f"not a finite number: {raw!r}{where}")
    return -v if neg else v


def load_series(csv_path, metrics, group_col, label_col=""):
    """One (metrics, days) matrix per group, plus the total across all of them.

    Only the campaigns the export says are switched on are trained on. A paused
    campaign's next days are a decision rather than a forecast, so it is not one
    the forecaster is ever asked about; fitting the adapter to its flat zeros
    spends training steps on series that will never be predicted. Groups that
    never moved are skipped for the same reason.

    The account total is the sum of every row, switched on or not, because that
    is what the account actually spent.

    group_col may be an ID column, which is what keeps a renamed campaign one
    series instead of two. label_col then says which column holds the readable
    name, used only so the skipped list names campaigns rather than numbers --
    grouping, running and stopped are all decided on the ID, exactly as ingest.go
    decides them.
    """
    import numpy as np
    rows = list(csv.reader(open(csv_path)))
    hdr, data = rows[0], rows[1:]
    ix = {n: i for i, n in enumerate(hdr)}

    missing = [m for m in metrics if m not in ix]
    if missing:
        sys.exit(f"columns not in {csv_path}: {missing}\navailable: {hdr}")

    days = sorted({r[0] for r in data})
    di = {d: i for i, d in enumerate(days)}

    per, total = {}, np.zeros((len(metrics), len(days)))
    for r in data:
        key = r[ix[group_col]] if group_col and group_col in ix else "(account)"
        m = per.setdefault(key, np.zeros((len(metrics), len(days))))
        for k, name in enumerate(metrics):
            v = num(r[ix[name]], f" in column {name!r} on {r[0]}")
            m[k][di[r[0]]] += v
            total[k][di[r[0]]] += v

    running = running_groups(hdr, data, group_col) if group_col else None

    # A campaign whose rows stop before the file does has stopped running. Some
    # exports say so with a status column, which running_groups reads; one that
    # lists a campaign only on the days it ran says it by leaving the rows out.
    # Either way the forecaster will not be asked about it (ingest.go's d.Stopped),
    # so fitting the adapter to its tail of zeros spends steps on a series nobody
    # will ever see. The two sides have to agree about this or training and
    # forecasting are about different campaigns.
    # The name each group goes by now: the one on the last day it appears.
    label = {}
    if label_col and label_col in ix and group_col and group_col in ix:
        seen_day = {}
        for r in data:
            k = r[ix[group_col]]
            if r[0] >= seen_day.get(k, ""):
                seen_day[k], label[k] = r[0], r[ix[label_col]]

    stopped = set()
    if group_col and group_col in ix:
        last_seen = {}
        for r in data:
            k = r[ix[group_col]]
            if r[0] > last_seen.get(k, ""):
                last_seen[k] = r[0]
        stopped = {k for k, d in last_seen.items() if d < days[-1]}
    out, skipped = [("(account)", total.astype("float32"))], []
    for k, m in per.items():
        if k == "(account)":
            continue
        if m.sum() <= 0 or k in stopped or (running is not None and k not in running):
            skipped.append(k)
            continue
        out.append((k, m.astype("float32")))
    if skipped:
        shown = sorted(label.get(k, k) for k in skipped)
        print(f"not training on {len(skipped)} switched-off, stopped or never-active "
              f"campaign(s): {', '.join(shown)}")
    return out, days


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("csv")
    ap.add_argument("--metrics", default=",".join(DEFAULT_METRICS))
    ap.add_argument("--group", default="Campaign", help="column separating campaigns")
    ap.add_argument("--label", default="", help="column holding the readable name, "
                    "when --group is an ID column")
    ap.add_argument("--steps", type=int, default=1000)
    ap.add_argument("--batch-size", type=int, default=8)
    ap.add_argument("--context", type=int, default=512)
    ap.add_argument("--lr", type=float, default=1e-4, help="LoRA wants a higher rate than full")
    ap.add_argument("--horizon", type=int, default=7)
    args = ap.parse_args()

    metrics = [m.strip() for m in args.metrics.split(",") if m.strip()]
    base = json.load(open(os.path.join(HERE, "weights.json")))["chronos2"]

    series, days = load_series(args.csv, metrics, args.group, args.label)
    trained_through = days[-1]
    print(f"training on {len(series)} series x {len(days)} days x {len(metrics)} metrics")
    print(f"data runs {days[0]} .. {trained_through}")

    # Nothing here judges the data and nothing here stops. --steps is the whole
    # instruction and every one of them runs, whatever the series count, however
    # long it takes. An earlier version printed a warning when there were fewer
    # than 50 series; it could not act on it, the reader had no way to act on it
    # either, and it read as a failure in the middle of a successful run. Whether
    # the adapter actually helps is a question for `accuracy`, which measures it
    # against days the model never saw -- not for a guess made before training.
    from transformers.trainer_callback import TrainerCallback

    # Counts steps so the registry can record what was actually run. Training is
    # never cut short: --steps is the whole instruction, and a clock that ended
    # it early would leave an undertrained adapter that looks like a finished one.
    class StepCount(TrainerCallback):
        def __init__(s): s.steps = 0
        def on_step_end(s, a, st, c, **k):
            s.steps += 1
            return c

    from chronos import Chronos2Pipeline
    pipe = Chronos2Pipeline.from_pretrained(base["path"], device_map="cpu")

    cb = StepCount()
    t0 = time.time()
    ft = pipe.fit([{"target": m} for _, m in series],
                  prediction_length=args.horizon, context_length=args.context,
                  num_steps=args.steps, batch_size=args.batch_size, learning_rate=args.lr,
                  finetune_mode="lora", output_dir=os.path.join(HERE, "finetuned", "_work"),
                  callbacks=[cb], remove_printer_callback=True)
    took = time.time() - t0

    os.makedirs(os.path.dirname(OUT_DIR), exist_ok=True)
    ft.save_pretrained(OUT_DIR)
    adapter = os.path.join(OUT_DIR, "adapter_model.safetensors")
    if not os.path.isfile(adapter):
        sys.exit(f"no adapter written to {OUT_DIR}")

    json.dump({"chronos2ft": {
        # relative to models/, so moving the project does not break it
        "path": os.path.relpath(OUT_DIR, HERE),
        "adapter_sha256": sha256(adapter),
        "bytes": os.path.getsize(adapter),
        "base_repo": base["repo"],
        "base_revision": base["revision"],
        "base_weights_sha256": base["weights_sha256"],
        "finetune_mode": "lora",
        "trained_through": trained_through,
        "trained_from": days[0],
        "train_series": len(series),
        "train_metrics": metrics,
        "steps": cb.steps,
        "batch_size": args.batch_size,
        "context_length": args.context,
        "learning_rate": args.lr,
        "train_seconds": round(took, 1),
        "trained_at": datetime.now().astimezone().isoformat(timespec="seconds"),
        "source_csv": os.path.abspath(args.csv),
    }}, open(REGISTRY, "w"), indent=2, sort_keys=True)

    print(f"\n{cb.steps} steps in {took:.0f}s")
    # the intermediate trainer output is large and never needed again
    import shutil
    shutil.rmtree(os.path.join(HERE, "finetuned", "_work"), ignore_errors=True)
    print(f"adapter: {os.path.getsize(adapter)/1e6:.1f} MB -> {OUT_DIR}")
    print(f"registered in models/finetuned.json, trained through {trained_through}")


if __name__ == "__main__":
    main()
