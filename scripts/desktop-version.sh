#!/usr/bin/env bash
# Stamp the desktop app's version for a release build.
#
#   scripts/desktop-version.sh <tag>            set info.productVersion in gui/wails.json
#   scripts/desktop-version.sh --ldflags <tag>  print -X ldflags for main.version/commit/date
#
# productVersion is the tag without its leading "v" (Windows file version and
# the macOS bundle version); the binary itself reports the full tag, the
# commit, and the commit date (see gui/version.go and the App.Version binding).
set -euo pipefail
cd "$(dirname "$0")/.."

if [ "${1:-}" = --ldflags ]; then
  tag=${2:?usage: scripts/desktop-version.sh --ldflags <tag>}
  commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo none)
  date=$(git log -1 --format=%cI 2>/dev/null || echo unknown)
  printf '%s' "-X main.version=$tag -X main.commit=$commit -X main.date=$date"
  exit 0
fi

tag=${1:?usage: scripts/desktop-version.sh <tag>}
python3 - "$tag" gui/wails.json <<'PY'
import json, sys
tag, path = sys.argv[1], sys.argv[2]
with open(path) as f:
    cfg = json.load(f)
cfg.setdefault("info", {})["productVersion"] = tag[1:] if tag.startswith("v") else tag
with open(path, "w") as f:
    json.dump(cfg, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY
