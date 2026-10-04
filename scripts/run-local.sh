#!/usr/bin/env bash
# Run the agent and dashboard locally for a real-hardware demo
# (meant for a DGX Spark host). Ctrl+C stops both.
set -euo pipefail
cd "$(dirname "$0")/.."

go build -o dist/native/spark-agent ./cmd/spark-agent
go build -o dist/native/spark-dash ./cmd/spark-dash

# Optionally build the PAIR bridge children from the local PAIR checkout.
PAIR_SRC=${PAIR_SRC:-$HOME/repo/Personal-AI-Router}
REPO="$PWD"
if [ -d "$PAIR_SRC/services" ]; then
    mkdir -p dist/native/pair
    for svc in nvpair-cluster-manager nvpair-node-scanner nvpair-workload-manager; do
        go build -C "$PAIR_SRC/services/$svc" -trimpath -ldflags "-s -w" \
            -o "$REPO/dist/native/pair/$svc" .
    done
    echo "PAIR children built into dist/native/pair (use --pair-enabled --pair-binaries-dir dist/native/pair)"
fi

trap 'kill "$AGENT_PID" "$DASH_PID" 2>/dev/null || true' EXIT

./dist/native/spark-agent --addr 127.0.0.1:9105 &
AGENT_PID=$!
./dist/native/spark-dash --addr :8080 --nodes "local=http://127.0.0.1:9105" &
DASH_PID=$!

echo
echo "dashboard: http://localhost:8080  (agent: http://127.0.0.1:9105/metrics)"
echo
wait "$DASH_PID"
