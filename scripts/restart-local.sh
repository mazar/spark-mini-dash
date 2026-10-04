#!/usr/bin/env bash
# Rebuild and restart the local two-node dashboard (spark-5a4e display host).
set -euo pipefail
cd "$(dirname "$0")/.."

go build -o dist/native/spark-dash ./cmd/spark-dash
pkill -f "dist/native/spark-dash" || true
sleep 1

(./dist/native/spark-dash --addr 127.0.0.1:8080 \
    --nodes "spark-5a4e=http://127.0.0.1:9105,spark-8a27=http://spark-8a27:9105" \
    --pair-enabled --pair-binaries-dir dist/native/pair \
    --pair-state-dir /tmp/spark-pair-test > /tmp/spark-dash2.log 2>&1 &)
echo "dashboard restarted on http://localhost:8080"
