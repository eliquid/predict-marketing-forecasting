"""Chronos-2 fine-tuned on your own data (LoRA adapter).

Same protocol and the same capabilities as chronos2_worker.py. The difference is
what it loads: the pinned base weights, plus a small LoRA adapter trained by
models/finetune.py.

The handshake carries `trained_through` -- the last day of data it was trained
on. Scoring this model on days at or before that date measures memorisation, not
forecasting, and the forecast_accuracy view excludes them for that reason.

NOTE: this file must NOT be named chronos.py or chronos2.py. A script's own
directory goes first on sys.path and would shadow the installed package.
"""
import json, os, sys

HERE = os.path.dirname(os.path.abspath(__file__))
os.environ["HF_HOME"] = os.path.join(HERE, "cache")
os.environ["HF_HUB_OFFLINE"] = "1"

QUANTILES = [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9]

# The protocol owns stdout; anything a library prints goes to stderr instead.
_OUT = os.fdopen(os.dup(sys.stdout.fileno()), "w")
sys.stdout = sys.stderr


def reply(obj):
    _OUT.write(json.dumps(obj) + "\n")
    _OUT.flush()


def _under_models(rel):
    """Resolve a registry path, refusing anything that leaves models/."""
    p = os.path.normpath(os.path.join(HERE, rel))
    root = os.path.normpath(HERE)
    if os.path.isabs(rel) or not (p == root or p.startswith(root + os.sep)):
        sys.exit(f"chronos2ft: models/finetuned.json points at {rel!r}, which is "
                 f"outside models/. The registry says where the adapter is, so it "
                 f"has to stay inside the folder it describes. Retrain, or move the "
                 f"adapter back under models/finetuned/.")
    return p


def load_registered():
    """Read models/finetuned.json and verify the adapter is the one recorded.

    Only the adapter weights are checksummed. adapter_config.json holds an
    absolute path to the base model, which has to be rewritten whenever the
    project moves, so it cannot be part of a fixed checksum.
    """
    import hashlib
    reg = os.path.join(HERE, "finetuned.json")
    if not os.path.isfile(reg):
        sys.exit("chronos2ft: no fine-tuned model yet. Train one with:\n"
                 "  models/.venv/bin/python models/finetune.py YOUR.csv")
    try:
        meta = json.load(open(reg))["chronos2ft"]
    except (ValueError, KeyError) as e:
        sys.exit(f"chronos2ft: models/finetuned.json is unreadable ({e}) -- retrain")

    # path is stored relative to models/ so the project can be moved -- and it is
    # held to that. An absolute value replaces HERE entirely and "../" escapes it,
    # so the registry could point the adapter anywhere on disk while only
    # adapter_model.safetensors is checksummed and everything else in that
    # directory (adapter_config.json, whatever from_pretrained picks up) is not.
    # The worker then writes adapter_config.json back into wherever it was sent.
    meta["path"] = _under_models(meta["path"])
    adapter = os.path.join(meta["path"], "adapter_model.safetensors")
    if not os.path.isfile(adapter):
        sys.exit(f"chronos2ft: adapter missing at {adapter} -- retrain")

    h = hashlib.sha256()
    with open(adapter, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    got = h.hexdigest()
    if got != meta["adapter_sha256"]:
        sys.exit(f"chronos2ft: the adapter on disk is not the one that was trained.\n"
                 f"  expected {meta['adapter_sha256']}\n  found    {got}\n"
                 f"Refusing to forecast with weights that cannot be identified.")

    # Point the adapter at wherever the base weights are now.
    cfg_path = os.path.join(meta["path"], "adapter_config.json")
    cfg = json.load(open(cfg_path))
    base = json.load(open(os.path.join(HERE, "weights.json")))["chronos2"]
    if cfg.get("base_model_name_or_path") != base["path"]:
        cfg["base_model_name_or_path"] = base["path"]
        json.dump(cfg, open(cfg_path, "w"), indent=2, sort_keys=True)
    return meta, base


def main():
    meta, base = load_registered()

    import numpy as np
    from chronos import Chronos2Pipeline
    from importlib.metadata import version

    p = Chronos2Pipeline.from_pretrained(meta["path"], device_map="cpu")

    reply({"model": "chronos2ft",
           "repo": base["repo"] + " + local LoRA",
           "revision": base["revision"],
           "weights_sha256": meta["adapter_sha256"],
           "covariates": True,
           "quantiles": QUANTILES,
           # everything below is extra: Go ignores unknown fields, and the whole
           # line is stored verbatim on every run, so provenance keeps it
           "finetune_mode": meta["finetune_mode"],
           "trained_through": meta["trained_through"],
           # The file it was fitted to, so the forecaster can say whose numbers
           # this adapter learned from. Basename only: the report is shareable and
           # an absolute path names the machine it was trained on.
           "trained_on": os.path.basename(meta.get("source_csv", "")),
           "trained_from": meta["trained_from"],
           "train_series": meta["train_series"],
           "train_metrics": meta["train_metrics"],
           "steps": meta["steps"],
           "trained_at": meta["trained_at"],
           "base_weights_sha256": meta["base_weights_sha256"],
           "versions": {n: version(n) for n in
                        ("chronos-forecasting", "peft", "torch", "numpy")}})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        req = json.loads(line)
        try:
            horizon = int(req["horizon"])
            series = np.asarray(req["series"], dtype=np.float32)
            if series.ndim != 2:
                raise ValueError(f"expected a (metrics, days) matrix, got {series.shape}")
            item = {"target": series}

            fut = req.get("future_covariates") or {}
            past = req.get("past_covariates") or {}
            if fut:
                missing = sorted(set(fut) - set(past))
                if missing:
                    raise ValueError(f"no history supplied for known-future {missing}")
                for k, v in past.items():
                    if len(v) != series.shape[1]:
                        raise ValueError(
                            f"history of {k!r} has {len(v)} values, series has {series.shape[1]}")
                item["past_covariates"] = {k: np.asarray(v, dtype=np.float32)
                                           for k, v in past.items()}
                item["future_covariates"] = {k: np.asarray(v, dtype=np.float32)
                                             for k, v in fut.items()}

            q, _mean = p.predict_quantiles([item], prediction_length=horizon,
                                           quantile_levels=list(req["quantiles"]))
            a = np.asarray(q[0], dtype=float)
            if a.ndim != 3 or a.shape[0] != series.shape[0]:
                raise ValueError(f"model returned {a.shape} for {series.shape[0]} metrics")
            reply({"id": req["id"], "quantiles": a.tolist()})
        except Exception as e:
            reply({"id": req.get("id", "?"), "error": f"{type(e).__name__}: {e}"})


if __name__ == "__main__":
    main()
