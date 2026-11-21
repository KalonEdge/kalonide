#!/bin/bash
set -e
go build -o "$TMPDIR/echoer.bin" .
out=$("$TMPDIR/echoer.bin" --upper hello world)
[ "$out" = "HELLO WORLD" ] || { echo "--upper gave: $out"; exit 1; }
out=$("$TMPDIR/echoer.bin" hello World)
[ "$out" = "hello World" ] || { echo "plain gave: $out"; exit 1; }
