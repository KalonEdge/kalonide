#!/bin/bash
grep -q "8087" "$ANSWER_FILE" || { echo "answer lacks 8087"; exit 1; }
grep -q "DASH_LISTEN_PORT" "$ANSWER_FILE" || { echo "answer lacks DASH_LISTEN_PORT"; exit 1; }
