#!/usr/bin/env python3
"""Fail if a relative link in a tracked Markdown file points at a file or heading that does not exist."""
import re, subprocess, sys, os

def slug(h):
    h = re.sub(r"[`*_~]", "", h.strip().lower())
    h = re.sub(r"[^\w\- ]", "", h)
    return h.replace(" ", "-")

files = subprocess.run(["git", "ls-files", "*.md"], capture_output=True, text=True, check=True).stdout.split()
bad = []
for f in files:
    text = open(f, encoding="utf-8").read()
    text = re.sub(r"```.*?```", "", text, flags=re.S)
    for m in re.finditer(r"\[[^\]]*\]\(([^)\s]+)\)", text):
        target = m.group(1)
        if re.match(r"^[a-z]+:", target):
            continue
        path, _, anchor = target.partition("#")
        dest = os.path.normpath(os.path.join(os.path.dirname(f), path)) if path else f
        if path and not os.path.exists(dest):
            bad.append(f"{f}: {target} (missing {dest})")
            continue
        if anchor and dest.endswith(".md") and os.path.isfile(dest):
            heads = {slug(h) for h in re.findall(r"^#+\s+(.*)$", open(dest, encoding="utf-8").read(), flags=re.M)}
            if anchor.lower() not in heads:
                bad.append(f"{f}: {target} (no heading #{anchor} in {dest})")
if bad:
    print("Broken links:"); print("\n".join(bad)); sys.exit(1)
print("ok")
