// newline-delimited JSON-RPC 2.0 over a child process's stdio — the local
// dialect every nvpair-* service speaks (cluster-manager README:
// "Newline-delimited JSON-RPC 2.0, one object per \n").
package pairbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) err() error {
	if e == nil {
		return nil
	}
	if len(e.Data) > 0 {
		return fmt.Errorf("jsonrpc %d: %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Errorf("jsonrpc %d: %s", e.Code, e.Message)
}

// rpcConn is one supervised child: its process, its stdin writer, and a
// dispatch loop for its stdout. Responses are matched by id; notifications
// go to the conn's Notify handler.
type rpcConn struct {
	name string
	cmd  *exec.Cmd

	writeMu sync.Mutex
	stdin   io.WriteCloser

	mu      sync.Mutex
	pending map[int64]chan rpcMessage
	nextID  int64
	onNotify func(method string, params json.RawMessage)
	onExit   func()

	closed chan struct{}
}

// spawnRPC starts name/args wired to stdio and begins draining its stdout.
// onNotify/onExit may be nil; onExit fires once after the process reaps.
func spawnRPC(name string, args []string, onNotify func(string, json.RawMessage), onExit func()) (*rpcConn, error) {
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = nil // children log to their own sinks; keep our log clean
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	c := &rpcConn{
		name:     name,
		cmd:      cmd,
		stdin:    stdin,
		pending:  make(map[int64]chan rpcMessage),
		onNotify: onNotify,
		onExit:   onExit,
		closed:   make(chan struct{}),
	}
	go c.readLoop(stdout)
	go c.wait()
	return c, nil
}

// Call sends an id-bearing request and waits for its result. A negative
// outcome still returns (err == nil) — PAIR reports failures as successful
// results with a terminal state; Call only errors on transport/rpc-level
// problems. ctx bounds the wait (pairing calls block on network I/O).
func (c *rpcConn) Call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(&rpcMessage{JSONRPC: "2.0", ID: mustRaw(id), Method: method, Params: marshal(params)}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error.err()
		}
		if result != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("%s: decode result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("%s: %s exited", method, c.name)
	}
}

// Notify sends a notification (no id, no response expected).
func (c *rpcConn) Notify(method string, params any) error {
	return c.write(&rpcMessage{JSONRPC: "2.0", Method: method, Params: marshal(params)})
}

func (c *rpcConn) write(msg *rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *rpcConn) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // directory snapshots can be large
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // skip non-protocol noise on stdout
		}
		if msg.Method != "" {
			if c.onNotify != nil {
				c.onNotify(msg.Method, msg.Params)
			}
			continue
		}
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if ok {
			ch <- msg
		}
	}
}

// wait reaps the process and fails every pending call.
func (c *rpcConn) wait() {
	_ = c.cmd.Wait()
	close(c.closed)
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if c.onExit != nil {
		c.onExit()
	}
}

// Stop closes stdin — the documented clean shutdown ("the manager exits
// cleanly on stdin EOF") — and gives the child a moment to exit.
func (c *rpcConn) Stop() {
	c.writeMu.Lock()
	_ = c.stdin.Close()
	c.writeMu.Unlock()
	select {
	case <-c.closed:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill()
	}
}

func marshal(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

func mustRaw(id int64) json.RawMessage {
	data, _ := json.Marshal(id)
	return data
}
