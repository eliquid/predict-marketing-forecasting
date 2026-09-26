"""Verify a model's weights before using them.

The whole point of recording a weights sha256 on every run is to be able to say
what produced a number. Copying the value out of weights.json proves nothing --
if the file on disk changed, every run would carry a sha that no longer describes
it. So it is recomputed at startup.

Cost is small next to loading the model: ~0.2s for Chronos-2, ~0.7s for TimesFM 3.0.

NOTE: not named weights.py or anything a package might be called -- a script's own
directory goes first on sys.path and would shadow the real package.
"""
import hashlib, json, os, sys


try:
    from fetch import EXPECTED
except Exception:  # fetch.py imports huggingface_hub, which a worker need not have
    EXPECTED = {}


def load_verified(here, name):
    """Return the entry for `name` from weights.json, after checking the file."""
    path = os.path.join(here, "weights.json")
    if not os.path.isfile(path):
        sys.exit(f"{name}: models/weights.json is missing -- run `predictmarketing setup`")
    try:
        meta = json.load(open(path))[name]
    except (ValueError, KeyError) as e:
        sys.exit(f"{name}: models/weights.json is unreadable ({e}) -- re-run `predictmarketing setup`")

    weights = os.path.join(meta["path"], "model.safetensors")
    if not os.path.isfile(weights):
        sys.exit(f"{name}: weights missing at {weights} -- run `predictmarketing setup`")

    h = hashlib.sha256()
    with open(weights, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    got = h.hexdigest()
    # Against the hash pinned in the source, not the one written beside the file.
    #
    # weights.json is mode 644 and sits next to the code, and it holds BOTH the
    # path and the hash -- so anything able to write that one file could point the
    # model at a substitute and record its hash in the same breath, and every check
    # here would pass. The run then stores that hash as provenance and states
    # confidently what produced a number it never saw. fetch.py's EXPECTED is the
    # pin that arrived with the source, so that is what the file is held to.
    pinned = EXPECTED.get(name)
    if pinned and got != pinned:
        sys.exit(
            f"{name}: these weights are not the ones this version of the tool pins.\n"
            f"  pinned in models/fetch.py  {pinned}\n"
            f"  found on disk             {got}\n"
            f"  file {weights}\n"
            f"Refusing to forecast with weights that cannot be identified. "
            f"Re-run `predictmarketing setup` to fetch them again.")
    if got != meta["weights_sha256"]:
        sys.exit(
            f"{name}: the weights on disk are not the ones that were downloaded.\n"
            f"  expected sha256 {meta['weights_sha256']}\n"
            f"  found    sha256 {got}\n"
            f"  file {weights}\n"
            f"Refusing to forecast with weights that cannot be identified. "
            f"Re-run `predictmarketing setup` to fetch them again.")
    return meta
