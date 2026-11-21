#!/bin/bash
grep -q "internal/conf/load.go" "$ANSWER_FILE" || { echo "wrong or missing path"; exit 1; }
