"""Self-check for the parts of finetune.py that need no model weights.

    python3 models/test_finetune.py

Plain stdlib on purpose, so it runs without models/.venv being installed. It
covers running_groups, which decides what the adapter is fitted to; getting that
wrong trains the model on campaigns the forecaster will never ask about.
"""
import os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from finetune import running_groups  # noqa: E402

# Shaped like a real Google Ads export: a campaign-status column next to a
# serving-status column, which must not be mistaken for it.
HDR = ["Day", "Campaign status", "Campaign", "Currency code", "Status",
       "Status reasons", "Cost", "Clicks"]
ROWS = [
    ["2026-01-01", "Enabled", "Brand", "USD", "Eligible (Limited)", "limited by budget", "10.00", "3"],
    ["2026-01-01", "Enabled", "Zombie", "USD", "Eligible", " --", "4.00", "1"],
    ["2026-01-01", "Paused", "Old", "USD", "Paused", " --", "0.00", "0"],
    # Last day: Zombie has since been switched off.
    ["2026-01-02", "Enabled", "Brand", "USD", "Eligible (Limited)", "limited by budget", "12.00", "4"],
    ["2026-01-02", "Paused", "Zombie", "USD", "Paused", " --", "0.00", "0"],
    ["2026-01-02", "Paused", "Old", "USD", "Paused", " --", "0.00", "0"],
]


def main():
    got = running_groups(HDR, ROWS, "Campaign")
    assert got == {"Brand"}, f"last day decides: {got}"

    # The serving-status column alone must not be read as campaign state.
    only_serving = [r[:1] + r[2:] for r in ROWS]
    hdr = HDR[:1] + HDR[2:]
    assert running_groups(hdr, only_serving, "Campaign") is None, \
        "'Eligible (Limited)' is not a campaign state and must not pick that column"

    # No status column at all: the caller falls back to the never-moved rule.
    plain = [[r[0], r[2], r[6]] for r in ROWS]
    assert running_groups(["Day", "Campaign", "Cost"], plain, "Campaign") is None

    # A file whose only states column is the campaign one, spelt differently.
    alt = [[r[0], r[2], "Active" if r[1] == "Enabled" else "Removed"] for r in ROWS]
    assert running_groups(["Day", "Campaign", "State"], alt, "Campaign") == {"Brand"}

    assert running_groups(HDR, ROWS, "Nope") is None, "unknown group column"
    print("ok")


if __name__ == "__main__":
    main()
