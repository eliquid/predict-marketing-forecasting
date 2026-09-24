#!/bin/sh
# Make a copy of this project to send to someone else.
#
#     ./share.sh ~/Dropbox/predict-marketing
#
# Leaves out the Python environment, the model weights and your own databases --
# about 2.5 GB that the other person's ./install.sh downloads fresh anyway.
#
# Also leaves out any fine-tuned adapter. It was fitted to YOUR numbers and is
# registered by a path on YOUR machine, so it would arrive broken and, if it did
# work, would be the wrong model for their data. They train their own.
set -eu
cd "$(dirname "$0")"

dest=${1:-}
[ -n "$dest" ] || { echo "usage: ./share.sh <folder to create>" >&2; exit 1; }
[ -e "$dest" ] && { echo "$dest already exists; pick a new folder" >&2; exit 1; }

# Prebuilt programs let someone without Go install too. Refresh them if the code
# has changed since they were made.
if [ ! -d dist ] && command -v go >/dev/null 2>&1; then
	echo "building the prebuilt programs first..."
	./build-dist.sh
fi

mkdir -p "$dest"
tar cf - \
	--exclude='models/.venv' \
	--exclude='models/cache' \
	--exclude='models/weights.json' \
	--exclude='models/__pycache__' \
	--exclude='models/finetuned' \
	--exclude='models/finetuned.json' \
	--exclude='predictmarketing' \
	--exclude='predict-marketing-forecasting' \
	--exclude='*.db' --exclude='*.db-wal' --exclude='*.db-shm' \
	--exclude='*_forecast*.html' \
	--exclude='./data' \
	--exclude='.git' \
	--exclude='.install-check*' \
	. | (cd "$dest" && tar xf -)

echo
echo "Copied to: $dest"
echo "Size:      $(du -sh "$dest" | awk '{print $1}')"
echo
echo "Tell them to open a terminal in that folder and run:"
echo "    ./install.sh"
