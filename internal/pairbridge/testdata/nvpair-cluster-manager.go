// Fake nvpair-cluster-manager: enough JSON-RPC over stdio to drive the
// bridge's pairing flow deterministically in tests. Emits an invite shortly
// after the identity probe, pairs on respond-to-invite.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type req struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

type resp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

type notif struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func send(v any) {
	data, _ := json.Marshal(v)
	fmt.Println(string(data))
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	go func() {
		time.Sleep(150 * time.Millisecond)
		p, _ := json.Marshal(map[string]any{"inviteId": "inv-1", "fromNodeName": "FAKE-INVITER"})
		send(notif{JSONRPC: "2.0", Method: "cluster:invite-received", Params: p})
	}()

	for sc.Scan() {
		var r req
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		var result json.RawMessage
		switch r.Method {
		case "cluster:get-node-id":
			result, _ = json.Marshal(map[string]string{"nodeUuid": "uuid-self", "clusterId": ""})
		case "cluster:set-identity":
			result, _ = json.Marshal(map[string]string{})
		case "cluster:respond-to-invite":
			result, _ = json.Marshal(map[string]string{"inviteId": "inv-1", "state": "paired"})
		default:
			result, _ = json.Marshal(map[string]string{})
		}
		send(resp{JSONRPC: "2.0", ID: r.ID, Result: result})
	}
}
