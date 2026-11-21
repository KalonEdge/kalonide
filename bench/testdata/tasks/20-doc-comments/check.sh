#!/bin/bash
set -e
go vet ./... >/dev/null
go test ./... 2>&1
# Every exported func must be directly preceded by a comment naming it.
python3 -I - <<'PY'
import re, sys
lines = open("calc.go").read().splitlines()
missing = []
for i, l in enumerate(lines):
    m = re.match(r"func ([A-Z]\w*)\(", l)
    if m and not (i > 0 and lines[i - 1].startswith("// " + m.group(1))):
        missing.append(m.group(1))
if missing:
    print("missing doc comments:", ", ".join(missing)); sys.exit(1)
PY
