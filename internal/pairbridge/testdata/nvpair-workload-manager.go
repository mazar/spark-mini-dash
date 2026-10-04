// Fake nvpair-workload-manager: replays a canned lifecycle (queued → running
// → removed) on stdout so the bridge's receive path can be tested end-to-end.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type notif struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func send(method string, params any) {
	p, _ := json.Marshal(params)
	data, _ := json.Marshal(notif{JSONRPC: "2.0", Method: method, Params: p})
	fmt.Println(string(data))
}

func main() {
	go func() {
		time.Sleep(200 * time.Millisecond)
		send("workloads:upsert", map[string]any{"workloadInfo": map[string]any{
			"id": "j1", "model": "qwen3.5:4b", "engine": "ollama", "state": "queued",
			"originatedFrom": "uuid-origin", "createdAt": time.Now().UnixMilli(),
		}})
		time.Sleep(300 * time.Millisecond)
		now := time.Now().UnixMilli()
		send("workloads:upsert", map[string]any{"workloadInfo": map[string]any{
			"id": "j1", "model": "qwen3.5:4b", "engine": "ollama", "state": "running",
			"originatedFrom": "uuid-origin", "scheduledOn": "uuid-target",
			"createdAt": now - 500, "startedAt": now,
		}})
		time.Sleep(400 * time.Millisecond)
		send("workloads:remove", map[string]any{"workloadId": "j1", "originatedFrom": "uuid-origin"})
	}()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		// consume stdin; the real service handles discovery:nodes etc.
	}
}
