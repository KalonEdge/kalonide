#!/bin/bash
set -e
go vet ./... >/dev/null
go build ./...
go test ./... 2>&1
