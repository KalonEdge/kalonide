#!/bin/bash
set -e
out=$(python3 -E -s greet.py Ada)
[ "$out" = "Hello, Ada!" ] || { echo "default: $out"; exit 1; }
out=$(python3 -E -s greet.py Ada --count 3)
[ "$out" = $'Hello, Ada!\nHello, Ada!\nHello, Ada!' ] || { echo "count 3: $out"; exit 1; }
