"""TimesFM 3.0 worker.

Speaks the protocol in worker.go: one handshake line on startup, then one JSON
line in / one JSON line out per forecast. Loads weights from this project's own
cache by path, offline, so it can never reach the network or pick up a different
model than the one recorded in weights.json.

Does NOT accept known-future values -- TimesFM 3.0 has no covariate input. That
is declared in the handshake so Go refuses such a request rather than dropping it.
"""
# NOTE: this file must NOT be named timesfm3.py. Python puts a script's own
# directory first on sys.path, so a file with that name would shadow the
# installed timesfm3 package and break the import below.
import json, os, sys

HERE = os.path.dirname(os.path.abspath(__file__))
os.environ["HF_HOME"] = os.path.join(HERE, "cache")
os.environ["HF_HUB_OFFLINE"] = "1"

QUANTILES = [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9]   # what TimesFM returns


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
    meta = load_verified(HERE, "timesfm3")   # recomputes the sha256 before use

    import numpy as np, timesfm
    from importlib.metadata import version

    f = timesfm.TimesFM3Forecaster.from_pretrained(meta["path"], device="cpu")

    reply({"model": "timesfm3", "repo": meta["repo"], "revision": meta["revision"],
           "weights_sha256": meta["weights_sha256"], "covariates": False,
           "quantiles": QUANTILES,
           "versions": {p: version(p) for p in ("timesfm", "torch", "numpy")}})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        req = json.loads(line)
        try:
            if req.get("future_covariates") or req.get("past_covariates"):
                raise ValueError("TimesFM 3.0 does not accept covariates")
            if list(req["quantiles"]) != QUANTILES:
                raise ValueError(f"TimesFM 3.0 returns {QUANTILES}, was asked for {req['quantiles']}")

            # (metrics, days) -- TimesFM's documented multivariate call, unmodified.
            ctx = np.asarray(req["series"], dtype=np.float32)
            if ctx.ndim != 2:
                raise ValueError(f"expected a (metrics, days) matrix, got shape {ctx.shape}")
            out = f.predict(context=ctx, horizon=int(req["horizon"]), return_quantiles=True)
            q = np.asarray(out.quantiles, dtype=float)            # (metrics, horizon, 9)
            if q.ndim != 3 or q.shape[0] != ctx.shape[0]:
                raise ValueError(f"model returned {q.shape} for {ctx.shape[0]} metrics")
            reply({"id": req["id"], "quantiles": q.tolist()})
        except Exception as e:
            reply({"id": req.get("id", "?"), "error": f"{type(e).__name__}: {e}"})


if __name__ == "__main__":
    main()
