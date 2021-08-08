#!/bin/bash
set -euo pipefail
truncate -s0 dict.ndjson
python3 parse.py
python3 parse_republican.py
