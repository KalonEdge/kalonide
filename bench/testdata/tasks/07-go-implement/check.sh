#!/bin/bash
set -e
go vet ./... >/dev/null
go test ./... 2>&1
