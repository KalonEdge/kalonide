#!/bin/bash
[ -f count.txt ] || { echo "no count.txt"; exit 1; }
got=$(tr -d ' \n\r\t' < count.txt)
[ "$got" = "7" ] || { echo "count.txt says '$got', want 7"; exit 1; }
