# Can Go run these models without Python? — tested 2026-09-20

**Chronos-2: no. TimesFM 3.0: partly — the heavy maths yes, the rest no.**

All of it measured on an Apple M1, not reasoned about: torch 2.14.0, onnx 1.23.0,
onnxruntime 1.30.0, timesfm 3.0.2, chronos-forecasting 2.3.2.

Both exporters were tried: the legacy TorchScript one (`dynamo=False`) and the
modern `torch.export`-based one (`dynamo=True`, the default in torch 2.14).

---

## Why this mattered

Go has a working ONNX runtime (`yalue/onnxruntime_go`, actively maintained). A
clean export would cut what has to ship from ~700 MB of Python + torch down to a
binary, a shared library and a weights file. That is the whole portability
question, so it got a real test.

---

## Chronos-2 — no viable path

**Legacy exporter:** traces the whole model. Three unsupported ops (`nanmean`,
`arcsinh`, `sinh`), all inside one class (`InstanceNorm`, `chronos_bolt.py:97-136`);
replacements verified to change the output by **0**. Then `Unfold` forces a fixed
context length. Then onnxruntime rejects the graph — float indices into an
embedding `Gather`. Hand-patching that node makes it load, 478 MB, 3,134 nodes.

**And the numbers are wrong: 34% relative error** on the exact input it was traced
with, 5.7% on random data, 72% on trend+season. Batching crashes. It runs, and it
lies. Root cause not chased — most likely the hand-patch made it loadable without
making it correct.

**Modern exporter: fails earlier, and for a reason that can't be worked around.**
`torch.export` cannot capture the graph at all — `GuardOnDataDependentSymNode` at
`chronos2/model.py:463`:

```python
if torch.isnan(future_covariates).any():
    raise ValueError(...)
```

A Python branch on tensor *contents*. Tried neutralising it as "just validation" —
**it isn't.** Patching `isnan` produced NaN output, because the real
`future_covariates` tensor genuinely contains NaNs as placeholders for unknown
future values. The NaN handling is load-bearing model semantics, and a static
graph cannot express it.

## TimesFM 3.0 — the transformer exports correctly, nothing else does

**There is no single `forward()` to export.** Confirmed with a global module hook:
`predict()` never calls the model's own forward. It drives submodules directly from
Python — so patching, CPM revin, linear detrending, stitching, the decode loop and
the output head all live in Python orchestration code, outside any graph.

**The core transformer, though, exports and is correct.** 327,843,200 of the
330,710,976 params:

| | |
|---|---|
| `torch.export` capture | **passes** — no data-dependent guards |
| ONNX conversion | one missing op, `prims.prod` → registered as `ReduceProd` via `custom_translation_table` |
| onnxruntime | **loads** |
| **numbers vs torch** | **relative diff 2.19e-06** — float32 noise. Correct. |
| dynamic patch count | **fails** at `torch.export`. Frozen to one context length. |

So for TimesFM there is a real middle option: **ONNX runs the transformer
(verified correct), Go reimplements the orchestration around it.** That trades a
20-layer transformer you can't verify for a few hundred lines of pre/post-processing
you also can't verify — but it is a far smaller target, and the maths that dominates
runtime is exactly right.

The catch: the graph is locked to one context length, so you would have to commit to
a fixed context and pad to it.

---

## What this means for the plan

**Nothing changes yet. Keep the Python worker.** It is the only thing that produces
correct forecasts for *both* models today, and Chronos-2 has no path at all.

**Portability is a packaging problem, not a runtime one.** ONNX was the attempt to
fix it at the runtime layer; it fixes at most half of one model. Fix it at the
packaging layer instead — `uv sync` against a committed lockfile on the target
machine, or a container image if the targets aren't Macs.

**If TimesFM-only, no-Python ever becomes the goal**, the transformer export is a
genuine starting point and this is the evidence for it. It is a project, not an
afternoon, and it would leave Chronos-2 behind.

**The JSON seam keeps all of this open.** Any of these becomes another worker with
no Go changes.

---

## Reproducing

Probe scripts are in the session scratchpad (`onnxtest/`), not committed — throwaway
diagnostics, not project code. Environment:

    uv pip install torch==2.14.0 numpy timesfm==3.0.2 chronos-forecasting onnx onnxruntime onnxscript

Chronos-2 weights downloaded fresh. TimesFM weights read from the existing local
snapshot instead of re-downloading 1.2 GB; nothing there was modified.
