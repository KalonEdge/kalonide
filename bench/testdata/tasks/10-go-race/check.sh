#!/bin/bash
set -e
grep -q "go func" hits.go || { echo "concurrency removed"; exit 1; }
go vet ./... >/dev/null
go test -race -count=1 ./... 2>&1
