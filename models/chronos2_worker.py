"""Chronos-2 worker.

Same protocol as timesfm3.py. The difference that matters: Chronos-2 accepts
known-future values (next week's budget, a planned promotion), so it declares
covariates=True and Go will pass them through.
"""
# NOTE: this file must NOT be named chronos.py. Python puts a script's own
# directory first on sys.path, so a file with that name would shadow the
# installed chronos package and break the import below.
import json, os, sys

HERE = os.path.dirname(os.path.abspath(__file__))
os.environ["HF_HOME"] = os.path.join(HERE, "cache")
os.environ["HF_HUB_OFFLINE"] = "1"

QUANTILES = [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9]   # exact subset of its native 21


# The protocol owns stdout. Any library that prints -- a progress bar, a warning --
# would corrupt the JSON stream, so the real stdout is taken here and everything
# else is redirected to stderr, where it is harmless and still visible.
_OUT = os.fdopen(os.dup(sys.stdout.fileno()), "w")
sys.stdout = sys.stderr


def reply(obj):
    _OUT.write(json.dumps(obj) + "\n")
    _OUT.flush()


def main():
    from weights_check import load_verified
    meta = load_verified(HERE, "chronos2")   # recomputes the sha256 before use

    import numpy as np
    from chronos import Chronos2Pipeline
    from importlib.metadata import version

    p = Chronos2Pipeline.from_pretrained(meta["path"], device_map="cpu")

    reply({"model": "chronos2", "repo": meta["repo"], "revision": meta["revision"],
           "weights_sha256": meta["weights_sha256"], "covariates": True,
           "quantiles": QUANTILES,
           "versions": {n: version(n) for n in ("chronos-forecasting", "torch", "numpy")}})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        req = json.loads(line)
        try:
            horizon = int(req["horizon"])
            # (variates, days). Chronos-2 shares information between the variates
            # of one item, which is exactly what forecasting spend, impressions and
            # clicks together should do.
            series = np.asarray(req["series"], dtype=np.float32)
            if series.ndim != 2:
                raise ValueError(f"expected a (metrics, days) matrix, got shape {series.shape}")
            item = {"target": series}

            fut = req.get("future_covariates") or {}
            past = req.get("past_covariates") or {}
            if fut:
                # Chronos requires the history of any covariate whose future is given.
                # Go guarantees both are present and correctly sized; anything missing
                # here is a protocol bug, so fail rather than substitute.
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
            a = np.asarray(q[0], dtype=float)      # (variates, horizon, quantiles)
            if a.ndim != 3 or a.shape[0] != series.shape[0]:
                raise ValueError(f"model returned {a.shape} for {series.shape[0]} metrics")
            reply({"id": req["id"], "quantiles": a.tolist()})
        except Exception as e:
            reply({"id": req.get("id", "?"), "error": f"{type(e).__name__}: {e}"})


if __name__ == "__main__":
    main()
