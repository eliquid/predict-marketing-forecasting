"""Fine-tune Chronos-2 on your own data with LoRA, and register the result.

    models/.venv/bin/python models/finetune.py "Campaign report.csv" [--steps N]

Produces models/finetuned/chronos2ft/ (about 5 MB) and records what it was
trained on in models/finetuned.json. The base weights are never touched.

LoRA rather than full fine-tuning, measured on this machine:
  full  2.5 steps/s, 3.3 GB peak, 456 MB checkpoint
  lora  3.0 steps/s, 3.3 GB peak, 4.9 MB checkpoint
Both are comfortable; LoRA is chosen for the 99x smaller checkpoint, which makes
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


def load_series(csv_path, metrics, group_col):
    """One (metrics, days) matrix per group, plus the total. Skips groups that
    never moved -- a paused campaign teaches the model nothing."""
    import numpy as np
    rows = list(csv.reader(open(csv_path)))
    hdr, data = rows[0], rows[1:]
    ix = {n: i for i, n in enumerate(hdr)}

    missing = [m for m in metrics if m not in ix]
    if missing:
        sys.exit(f"columns not in {csv_path}: {missing}\navailable: {hdr}")

    days = sorted({r[0] for r in data})
    di = {d: i for i, d in enumerate(days)}
    num = lambda s: float(s.strip().replace(",", "").replace("$", "").replace("%", ""))

    per, total = {}, np.zeros((len(metrics), len(days)))
    for r in data:
        key = r[ix[group_col]] if group_col and group_col in ix else "(account)"
        m = per.setdefault(key, np.zeros((len(metrics), len(days))))
        for k, name in enumerate(metrics):
            v = num(r[ix[name]])
            m[k][di[r[0]]] += v
            total[k][di[r[0]]] += v

    out = [("(account)", total.astype("float32"))]
    for k, m in per.items():
        if k != "(account)" and m.sum() > 0:
            out.append((k, m.astype("float32")))
    return out, days


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("csv")
    ap.add_argument("--metrics", default=",".join(DEFAULT_METRICS))
    ap.add_argument("--group", default="Campaign", help="column separating campaigns")
    ap.add_argument("--steps", type=int, default=1000)
    ap.add_argument("--batch-size", type=int, default=8)
    ap.add_argument("--context", type=int, default=512)
    ap.add_argument("--lr", type=float, default=1e-4, help="LoRA wants a higher rate than full")
    ap.add_argument("--horizon", type=int, default=7)
    ap.add_argument("--budget", type=int, default=900, help="wall-clock seconds; a hard stop")
    args = ap.parse_args()

    metrics = [m.strip() for m in args.metrics.split(",") if m.strip()]
    base = json.load(open(os.path.join(HERE, "weights.json")))["chronos2"]

    series, days = load_series(args.csv, metrics, args.group)
    trained_through = days[-1]
    print(f"training on {len(series)} series x {len(days)} days x {len(metrics)} metrics")
    print(f"data runs {days[0]} .. {trained_through}")
    if len(series) < 50:
        print(f"NOTE: {len(series)} series is very little. Chronos-2 was pretrained on\n"
              f"      millions; measured on this data, fine-tuning on 7 series made the\n"
              f"      forecast WORSE than the stock model. Score it before trusting it.")

    from transformers.trainer_callback import TrainerCallback

    class Budget(TrainerCallback):
        def __init__(s): s.t0, s.steps, s.stopped = None, 0, False
        def on_train_begin(s, a, st, c, **k): s.t0 = time.time()
        def on_step_end(s, a, st, c, **k):
            s.steps += 1
            if time.time() - s.t0 > args.budget:
                c.should_training_stop = True
                s.stopped = True
            return c

    from chronos import Chronos2Pipeline
    pipe = Chronos2Pipeline.from_pretrained(base["path"], device_map="cpu")

    cb = Budget()
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
        "hit_time_budget": cb.stopped,
        "batch_size": args.batch_size,
        "context_length": args.context,
        "learning_rate": args.lr,
        "train_seconds": round(took, 1),
        "trained_at": datetime.now().astimezone().isoformat(timespec="seconds"),
        "source_csv": os.path.abspath(args.csv),
    }}, open(REGISTRY, "w"), indent=2, sort_keys=True)

    print(f"\n{cb.steps} steps in {took:.0f}s"
          f"{' (hit the time budget)' if cb.stopped else ''}")
    # the intermediate trainer output is large and never needed again
    import shutil
    shutil.rmtree(os.path.join(HERE, "finetuned", "_work"), ignore_errors=True)
    print(f"adapter: {os.path.getsize(adapter)/1e6:.1f} MB -> {OUT_DIR}")
    print(f"registered in models/finetuned.json, trained through {trained_through}")


if __name__ == "__main__":
    main()
