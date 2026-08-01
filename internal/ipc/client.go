package ipc

// Minimal Go IPC client for a standalone CLI process to reach a co-located
// `nightgauge serve` daemon over its workspace-scoped Unix socket (#263).
// One-shot request/response only — no retries, no reconnect, no
// subscription support — matching the CLI's one-shot per-invocation use.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client is a minimal one-shot JSON-RPC client over a Unix domain socket.
type Client struct {
	conn net.Conn
}

// DialClient dials the daemon socket at socketPath with the given timeout.
// A short timeout is appropriate: this is a same-host, same-filesystem local
// socket, so no daemon means ENOENT/ECONNREFUSED returns near-instantly —
// callers use the error to fall back to a local execution path without the
// CLI command feeling hung.
func DialClient(ctx context.Context, socketPath string, timeout time.Duration) (*Client, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// Call sends one JSON-RPC request and reads back exactly one response, per
// the socket transport's one-request-per-connection contract
// (serveSocketConn). On success, result (if non-nil) is populated by
// unmarshaling resp.Result. A daemon-side error (resp.Error) is surfaced as a
// Go error — this is a transport-successful, application-level rejection,
// distinct from a dial failure.
func (c *Client) Call(ctx context.Context, method string, params, result interface{}) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}

	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("ipc client: marshal params: %w", err)
	}
	req := Request{ID: 1, Method: method, Params: paramsRaw}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("ipc client: marshal request: %w", err)
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ipc client: write request: %w", err)
	}

	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("ipc client: read response: %w", err)
		}
		return fmt.Errorf("ipc client: no response from daemon")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("ipc client: parse response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("ipc client: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}
	if result != nil && resp.Result != nil {
		resultRaw, err := json.Marshal(resp.Result)
		if err != nil {
			return fmt.Errorf("ipc client: re-marshal result: %w", err)
		}
		if err := json.Unmarshal(resultRaw, result); err != nil {
			return fmt.Errorf("ipc client: decode result: %w", err)
		}
	}
	return nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
