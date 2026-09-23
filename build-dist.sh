#!/bin/sh
# Build the programs that install.sh falls back to when Go is not installed.
#
# Run this before sharing the folder with someone who may not have Go:
#
#     ./build-dist.sh
#
# Anyone WITH Go gets a fresh build from source instead, so these are only a
# convenience for people who have no toolchain at all.
set -eu
cd "$(dirname "$0")"
mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
	os=${target%/*}; arch=${target#*/}
	out="dist/predictmarketing-$os-$arch"
	GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$out" .
	printf '  %-34s %s\n' "$out" "$(ls -lh "$out" | awk '{print $5}')"
done
printf '%s\n' "built $(date +%Y-%m-%d) from this source"  > dist/BUILT.txt
echo "done."
