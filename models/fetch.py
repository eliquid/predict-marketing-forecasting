"""Download both models' weights into this project, at pinned revisions.

Run by `predictmarketing setup`. Downloads nothing that is already present and
verified, so it is safe to re-run.
"""
import os, sys, hashlib, json
from huggingface_hub import snapshot_download

HERE = os.path.dirname(os.path.abspath(__file__))
CACHE = os.path.join(HERE, "cache")

MODELS = {
    "timesfm3": ("google/timesfm-3.0-pytorch", "43046b85ec22d584a13f8098c2ed39c889e129c2"),
    "chronos2": ("amazon/chronos-2",           "29ec3766d36d6f73f0696f85560a422f50e8498c"),
}

# The sha256 each model's weights must have. Pinning the revision fixes what is
# downloaded; pinning the hash proves what arrived, which is what makes it safe
# to accept weights from anywhere other than Hugging Face.
EXPECTED = {
    "timesfm3": "a7592b0a8432baee54483254e5647856911ce69e09d09a9bb65904b2d98f17da",
    "chronos2": "ddcda3c7508bf2528087723e98a20707cc04b7f370ae275a9fd88078ddba4f42",
}

# Somewhere that Hugging Face is blocked, a model can be pointed at a directory
# already holding its weights: PM_CHRONOS2_DIR=/path/to/chronos-2. The hash is
# checked either way, so a local copy is held to exactly the same standard as a
# downloaded one. TimesFM has no equivalent on purpose -- its licence forbids
# redistribution, so there is nowhere to point it at but Google.
def local_override(name):
    return os.environ.get(f"PM_{name.upper()}_DIR", "").strip()

def sha256(path, buf=1 << 20):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(buf), b""):
            h.update(chunk)
    return h.hexdigest()

def main():
    os.makedirs(CACHE, exist_ok=True)
    out = {}
    for name, (repo, rev) in MODELS.items():
        override = local_override(name)
        if override:
            print(f"[{name}] using local weights at {override}", flush=True)
            path = os.path.abspath(os.path.expanduser(override))
            if not os.path.isdir(path):
                sys.exit(f"{name}: PM_{name.upper()}_DIR is not a directory: {path}")
        else:
            print(f"[{name}] {repo} @ {rev[:12]}", flush=True)
            path = snapshot_download(repo_id=repo, revision=rev, cache_dir=CACHE)
        w = os.path.join(path, "model.safetensors")
        if not os.path.isfile(w):
            sys.exit(f"{name}: no model.safetensors in {path}")
        digest = sha256(w)
        if digest != EXPECTED[name]:
            sys.exit(f"{name}: these are not the weights this build expects.\n"
                     f"  expected sha256 {EXPECTED[name]}\n"
                     f"  got             {digest}\n"
                     f"  at              {w}")
        size = os.path.getsize(w)
        print(f"[{name}] ok  {size/1e9:.2f} GB  sha256 {digest[:16]}...", flush=True)
        out[name] = {"repo": repo, "revision": rev, "path": path,
                     "weights_sha256": digest, "bytes": size}
    with open(os.path.join(HERE, "weights.json"), "w") as f:
        json.dump(out, f, indent=2, sort_keys=True)
    print("wrote models/weights.json")

if __name__ == "__main__":
    main()
