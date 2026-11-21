#!/bin/bash
set -e
out=$(python3 -E -s report.py)
[ "$out" = "Lovelace, Ada: \$1,234.50" ] || { echo "got: $out"; exit 1; }
