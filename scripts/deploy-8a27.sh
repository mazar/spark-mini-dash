#!/usr/bin/env bash
# Rebuild spark-dash (UI is embedded) and redeploy it to the spark-8a27 test
# host, restarting the pair-bridge instance there.
set -euo pipefail
cd "$(dirname "$0")/.."

go build -o dist/native/spark-dash ./cmd/spark-dash

ssh spark-8a27 "pkill -x spark-dash" || true
sleep 1
scp -q dist/native/spark-dash spark-8a27:~/spark-dash-test/

ssh spark-8a27 "nohup ~/spark-dash-test/spark-dash --addr :8081 --nodes 'local=http://127.0.0.1:9105' --pair-enabled --pair-binaries-dir /home/massoud/spark-dash-test/pair --pair-state-dir /home/massoud/spark-dash-test/state --pair-cluster-port 14321 --pair-workload-port 14320 > ~/spark-dash-test/dash.log 2>&1 & sleep 2; tail -2 ~/spark-dash-test/dash.log"
