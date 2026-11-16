#!/usr/bin/env python3
"""Check that relative Markdown links in this repository resolve to real files.

Usage (from the repository root):
    python scripts/check_md_links.py
    python scripts/check_md_links.py --root .

Ignores external URLs, in-page anchors, and fenced code blocks. Exits non-zero
when at least one relative link is broken, so it can gate commits.
"""
from __future__ import annotations

import argparse
import os
import re
import sys

LINK = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
SKIP_DIRS = {".git", "bin", "node_modules"}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    args = parser.parse_args()
    root = os.path.abspath(args.root)

    markdown_files: list[str] = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if name.endswith(".md"):
                markdown_files.append(os.path.join(dirpath, name))

    checked = 0
    missing: list[tuple[str, str]] = []
    for path in markdown_files:
        in_fence = False
        with open(path, encoding="utf-8") as handle:
            for line in handle:
                if line.strip().startswith("```"):
                    in_fence = not in_fence
                    continue
                if in_fence:
                    continue
                for match in LINK.finditer(line):
                    target = match.group(1).strip()
                    if not target or target.startswith(("http://", "https://", "mailto:", "#")):
                        continue
                    target = target.split("#", 1)[0].strip()
                    if not target:
                        continue
                    checked += 1
                    resolved = os.path.normpath(os.path.join(os.path.dirname(path), target))
                    if not os.path.exists(resolved):
                        missing.append((os.path.relpath(path, root), target))

    print(f"md_files={len(markdown_files)} relative_links_checked={checked} missing={len(missing)}")
    for source, target in missing:
        print(f"MISSING {source} -> {target}")
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main())
