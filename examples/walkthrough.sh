#!/bin/sh
# The single-file commands, run for real: forecast, models, runs.
#
# Not the recurring job -- `import`, `report` and `accuracy` are not exercised
# here. See AGENTS.md 2c for those.
#
#     ./examples/walkthrough.sh
#
# An agent can run this to see actual behaviour instead of trusting a document.
# It writes only into examples/ and a throwaway database.
set -eu
cd "$(dirname "$0")/.."
PM=./predictmarketing
DB=examples/walkthrough.db
[ -x "$PM" ] || { echo "build it first: go build -o predictmarketing ."; exit 1; }
rm -f "$DB" "$DB-wal" "$DB-shm"

step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

step "1. The simplest file: one metric, default model, default horizon of 7"
$PM forecast examples/01-simple.csv -db "$DB" 2>/dev/null | head -14

step "2. Several metrics at once -- all forecast in ONE model call"
$PM forecast examples/02-marketing.csv -model timesfm3 -db "$DB" 2>/dev/null | head -6

step "3. A real platform export: text column set aside, awkward names handled"
$PM forecast examples/03-platform-export.csv -model chronos2 -db "$DB" 2>/dev/null | head -5

step "4. Only some columns"
$PM forecast examples/03-platform-export.csv -columns "Cost,Clicks" -db "$DB" 2>/dev/null | head -4

step "5. Known-future values: budget is planned, so tell the model"
$PM forecast examples/04-with-budget.csv -model chronos2 \
    -future "budget=900,900,900,900,900,900,900" -db "$DB" 2>/dev/null | head -5

step "6. The same request on TimesFM, which cannot use them -- refused, not ignored"
$PM forecast examples/04-with-budget.csv -model timesfm3 \
    -future "budget=900,900,900,900,900,900,900" -db "$DB" 2>&1 >/dev/null | head -2 || true

step "7. What each model is"
$PM models 2>/dev/null

step "8. Everything that was run"
$PM runs -db "$DB" 2>/dev/null

step "Reports written"
ls examples/*_forecast_*.html 2>/dev/null || echo "  (none)"
echo
echo "Open one to see the charts. Clean up with:"
echo "  rm -f examples/*_forecast_*.html examples/walkthrough.db*"
