---
name: hand-off
description: Package this folder for someone else and check what you are actually sending them. Use before running share.sh, before publishing a release, or whenever a copy of the project leaves this machine.
---

# Handing the folder to someone else

`AGENTS.md` §2 is how *you* install it; this is how someone else does, from a
copy you made. `share.sh` is one command, but the command is not the procedure.

The thing to understand before anything else: **`share.sh` has its own exclusion
list, and it is not `.gitignore`.** They are maintained separately and have drifted
before. `share.sh` now excludes `./data` and `.git` wholesale, but it did not
always, and the report patterns it relies on for reports written *outside* `data/`
have stopped matching the tool's filenames twice. Check the bundle; do not assume
the list is current.

## 1. Scrub, then build, then copy — in that order

```bash
ls data/imported data/reports 2>/dev/null   # your exports and your reports
```

`data/imported/*.csv` is the raw export — every campaign name, every day of spend
— and `data/reports/*_models.html` names the same campaigns. Both are excluded by
`./data` now, and `TestShareScriptExcludesGeneratedFiles` asserts that exclusion
with all three real import filenames. Note what that pattern does **not** cover: a
report `forecast` wrote next to a CSV somewhere else is caught only by
`*_forecast*.html`, which is a filename pattern and has failed twice before.

Verify rather than trust — list the copy before you hand it over (step 3).

```bash
rm -rf dist                                  # or the old binaries get re-shipped
./share.sh ~/somewhere/predict-marketing
ls ~/somewhere/predict-marketing/data 2>/dev/null   # must print nothing
```

`rm -rf dist` matters: the guard in `share.sh` is `[ ! -d dist ]`, so it builds
the prebuilt binaries only when there are none. With `dist/` already present it
re-ships whatever is there, however old. `dist/BUILT.txt` records the date.

`.git` is excluded too, so their build reports no commit. If you *want* them to
have the history, copy it in deliberately afterwards — but
it is the *whole* history.

## 2. Check the copy before you send it

```bash
cd ~/somewhere/predict-marketing
ls data 2>/dev/null && echo "STOP: your data is in the bundle"
find . -name '*.db' -o -name '*_forecast*.html' -o -name '*_models.html' \
     -o -path './models/finetuned*'
```

That find must print nothing. A fine-tuned adapter in particular is fitted to
your numbers and registered by a path on your machine: it would arrive broken,
and if it worked it would be the wrong model for their data (`AGENTS.md` §4c).

Then prove the bundle builds and runs its own checks:

```bash
go build -o predictmarketing . && go vet ./... && go test -count=1 .
```

**Expect eighteen `--- SKIP` lines, and expect the suite to still say `ok`.**
Count them with `go test -count=1 -v . | grep -c -- '--- SKIP'`; a plain run
prints no skip count at all. A copy has no
`models/.venv`, so everything that needs a worker disappears: all eight
protocol tests, both weights tests, the multivariate perturbation test, and the
binary/pycache tests. A green run in a fresh bundle proves the CSV reader, the
database and the HTML — it proves nothing about the model protocol, which
`AGENTS.md` §4 calls the one thing not to break. That check only happens after
`./install.sh` on their machine, or on yours.

## 3. Prebuilt binaries: who they are for

`build-dist.sh` builds four: `darwin/arm64`, `darwin/amd64`, `linux/amd64`,
`linux/arm64`. They exist only so somebody with no Go toolchain can install.
Anyone with Go gets a fresh build from source instead, and that is the path
`install.sh` prefers.

There is no Windows binary on purpose. The program cross-compiles to Windows
cleanly, but `install.sh` and `share.sh` are POSIX `sh`, so there would be
nothing on Windows to run them. Send a Windows user to the by-hand steps in
`README.md`, or to WSL.

## 4. What to tell them

One command, in a terminal opened in the folder:

```bash
./install.sh
```

Three things on their first run look like faults and are not, and they will ask
about all three — `AGENTS.md` §2b has the detail: the Hugging Face
"unauthenticated requests" warning (both models are public and ungated), the
`Loading weights` progress bar on stderr, and the database arriving at mode
0600.

Tell them the third model is not in the box. `chronos2ft` is trained on their
own numbers and has nothing to learn from until they have a CSV; `install.sh`
says so at the end, and `.claude/skills/finetune/` is the procedure.

## 5. Done means

You looked inside the copy. Say what `find` printed and what `go test` reported,
including the skip count. "share.sh ran" is not done — the script has shipped
real campaign names before, and it is the one failure mode that cannot be taken
back once the folder is sent.
