#!/bin/sh
# Set up Predict Marketing. Run it once:
#
#     ./install.sh
#
# Safe to run again -- anything already done is skipped, so if a download fails
# you can just run it a second time.
#
# It needs about 3 GB of disk and downloads roughly 2.5 GB the first time.
#
# Sets up the two pretrained models. The third (chronos2ft) is fine-tuned on your
# own data, so it cannot be built here -- there is nothing to train on yet. Its
# dependencies are installed and the last message says how to train it.

set -eu
cd "$(dirname "$0")"

BOLD=''; DIM=''; OFF=''
if [ -t 1 ]; then BOLD=$(printf '\033[1m'); DIM=$(printf '\033[2m'); OFF=$(printf '\033[0m'); fi
say()  { printf '%s\n' "$BOLD$*$OFF"; }
note() { printf '%s\n' "$DIM  $*$OFF"; }
die()  { printf '\n%s\n' "${BOLD}Stopped: $*$OFF" >&2; exit 1; }

say "Predict Marketing - setup"
echo

# ---------------------------------------------------------------- disk space
need_gb=3
free_gb=$(df -Pk . | awk 'NR==2 {print int($4/1048576)}')
if [ "$free_gb" -lt "$need_gb" ]; then
	die "need about ${need_gb} GB free, this disk has ${free_gb} GB."
fi
note "disk space: ${free_gb} GB free, need about ${need_gb} GB"

# ---------------------------------------------------------------------- uv
# uv creates the Python environment and can fetch Python itself, so it is the
# only prerequisite. If it is missing we ask before installing it, rather than
# piping a script from the internet into your shell without telling you.
if ! command -v uv >/dev/null 2>&1; then
	if [ -x "$HOME/.local/bin/uv" ]; then
		PATH="$HOME/.local/bin:$PATH"; export PATH
	else
		echo
		say "uv is not installed."
		echo "  uv is the Python installer this project uses. It is made by Astral"
		echo "  and installs from:  https://astral.sh/uv/install.sh"
		echo
		printf "  Install it now? [y/N] "
		read -r reply </dev/tty 2>/dev/null || die "run this in a terminal, or install uv yourself first: https://docs.astral.sh/uv/"
		case "$reply" in
			[yY]*) ;;
			*) die "install uv yourself, then run this again: https://docs.astral.sh/uv/" ;;
		esac
		curl -LsSf https://astral.sh/uv/install.sh | sh || die "could not install uv"
		PATH="$HOME/.local/bin:$PATH"; export PATH
		command -v uv >/dev/null 2>&1 || die "uv installed but not on PATH; open a new terminal and run this again"
	fi
fi
note "uv: $(uv --version 2>/dev/null || echo present)"

# ------------------------------------------------------------ python + libs
say "1/4  Python environment (about 800 MB, includes fine-tuning support)"
if [ ! -x models/.venv/bin/python ]; then
	# 3.11 is the version this was tested with, so it is preferred when present.
	# Anything from 3.10 up is accepted otherwise: a version being untested is not
	# a reason to refuse to install. If the packages genuinely cannot run on it,
	# the install step below says so, which is a far more useful failure than a
	# version number compared against a hardcoded one.
	uv venv models/.venv --python 3.11 >/dev/null 2>&1 \
		|| uv venv models/.venv --python '>=3.10' >/dev/null \
		|| die "could not create a Python environment (3.10 or newer needed)"
fi
VIRTUAL_ENV="$PWD/models/.venv" uv pip install --quiet -r models/requirements.txt \
	|| die "could not install the Python packages"
note "$(models/.venv/bin/python -V)"

# ------------------------------------------------------------------- binary
say "2/4  The program itself"
if command -v go >/dev/null 2>&1; then
	go build -o predictmarketing . || die "could not build; is this the full project folder?"
	note "built from source with $(go version | awk '{print $3}')"
else
	os=$(uname -s | tr 'A-Z' 'a-z'); arch=$(uname -m)
	case "$arch" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
	pre="dist/predictmarketing-$os-$arch"
	[ -f "$pre" ] || die "Go is not installed and there is no prebuilt program for $os/$arch.
Install Go from https://go.dev/dl/ and run this again."
	cp "$pre" predictmarketing && chmod +x predictmarketing
	note "used the prebuilt program for $os/$arch"
	note "(a snapshot -- install Go and re-run this to build the current code)"
fi

# ------------------------------------------------------------------ weights
say "3/4  Model weights (about 1.7 GB, downloaded once)"
./predictmarketing setup || die "could not download the model weights"

# ------------------------------------------------------------------- verify
say "4/4  Checking it works"
./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 3 \
	-series install-check -db .install-check.db -out .install-check.html >/dev/null 2>&1 \
	|| die "the test forecast failed"
rm -f .install-check.db .install-check.db-wal .install-check.db-shm .install-check.html
note "a real forecast ran and was verified"

# The third model is trained on YOUR data, so it cannot exist until you have
# some. Everything it needs is installed; it just has nothing to learn from yet.
ready=$(./predictmarketing models 2>/dev/null | grep -c "^chronos2ft .*unavailable" || true)

echo
say "Done."
echo
echo "  Two models are ready now:"
echo "    chronos2    Amazon Chronos-2      (Apache-2.0)"
echo "    timesfm3    Google TimesFM 3.0    (non-commercial)"
if [ "$ready" != "0" ]; then
echo
echo "  A third is optional and trained on your own data:"
echo "    chronos2ft  Chronos-2 + a LoRA adapter fitted to your numbers"
echo "    Everything it needs is installed. It has nothing to learn from until"
echo "    you have a CSV, so train it whenever you like:"
echo "      models/.venv/bin/python models/finetune.py YOUR.csv --steps 2000 --budget 600"
echo "    Takes about 10 minutes. See .claude/skills/finetune/ before relying on it."
fi
echo
echo "  Try it now:"
echo "    ./predictmarketing forecast testdata/example.csv -model chronos2 -horizon 7"
echo
echo "  Then open the file it tells you about. Your own data goes in a CSV like:"
echo "    date,spend"
echo "    2026-03-01,359.64"
echo
echo "  More: ./predictmarketing --help   and   README.md"
