#!/bin/bash
set -e
if grep -rn "calcTotal" --include='*.go' . ; then echo "calcTotal still present"; exit 1; fi
go vet ./... >/dev/null
go test ./... 2>&1
