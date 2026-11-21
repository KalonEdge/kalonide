#!/bin/bash
# Passes when the agent's tests pass on the real code AND catch a broken
# byte-reversing version (so they really test Unicode).
set -e
ls *_test.go >/dev/null 2>&1 || { echo "no test file"; exit 1; }
grep -q "func Test" *_test.go || { echo "no Test function"; exit 1; }
go test ./... 2>&1
cp strutil.go "$TMPDIR/orig.go"
cp "$TASK_DIR/mutant/strutil.go" strutil.go
if go test ./... >/dev/null 2>&1; then cp "$TMPDIR/orig.go" strutil.go; echo "tests did not catch the broken version"; exit 1; fi
cp "$TMPDIR/orig.go" strutil.go
echo "tests catch the broken version"
