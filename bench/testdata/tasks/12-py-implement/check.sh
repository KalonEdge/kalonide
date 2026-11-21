#!/bin/bash
set -e
python3 -I -m unittest discover -q 2>&1
