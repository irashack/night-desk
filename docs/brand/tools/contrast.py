#!/usr/bin/env python3
"""WCAG 2.2 contrast for Night Desk's colours.

    python3 docs/brand/tools/contrast.py '#A63E22' '#F8EFE1'   # one pair
    python3 docs/brand/tools/contrast.py                       # every theme in themes.json

Text needs 4.5:1, rules and other non-text marks 3:1. web/themes_test.go runs
the same check on every build.
"""

import json
import sys
from pathlib import Path

PAIRS = [("p-ink", "p-bg", 4.5), ("p-muted", "p-bg", 4.5), ("p-accent", "p-bg", 4.5), ("p-rule", "p-bg", 3.0),
         ("fg", "bg", 4.5), ("muted", "bg", 4.5), ("fg", "chip", 4.5)]


def luminance(hex_colour):
    channels = [int(hex_colour[i:i + 2], 16) / 255 for i in (1, 3, 5)]
    linear = [c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4 for c in channels]
    return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2]


def contrast(a, b):
    la, lb = luminance(a), luminance(b)
    return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)


def main(args):
    if len(args) == 2:
        print(f"{contrast(*args):.2f}:1")
        return 0
    themes = json.loads((Path(__file__).resolve().parent.parent / "themes.json").read_text())["themes"]
    failed = 0
    for name, theme in themes.items():
        for mode in ("light", "dark"):
            for fg, bg, minimum in PAIRS:
                ratio = contrast(theme[mode][fg], theme[mode][bg])
                ok = ratio >= minimum
                failed += not ok
                print(f"{'ok  ' if ok else 'FAIL'} {name:13} {mode:5} {fg:8} on {bg:5} {ratio:5.2f}:1 (min {minimum})")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
