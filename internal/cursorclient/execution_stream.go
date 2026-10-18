package cursorclient

import (
	"context"
	"sync"
	"time"
)

// A single sequenced writer serves results and execution heartbeats. Append
// timeouts are terminal: an uncertain append must never be retried blindly.
func (c Client) runSender(ctx context.Context, requestID string) func(*Value) error {
	var mu sync.Mutex
	var seq int64
	return func(msg *Value) error {
		mu.Lock()
		defer mu.Unlock()
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := c.append(requestCtx, requestID, seq, msg); err != nil {
			return err
		}
		seq++
		return nil
	}
}

func execControl(req *Value, field, typ string) *Value {
	item, _ := New(typ)
	item.Set("id", req.Int("id"))
	control, _ := New("ExecClientControlMessage")
	control.Set(field, item)
	return wrapClient("exec_client_control_message", control)
}

// Cursor consumes execution replies as a stream even for unary tools. A shell
// exit is a result within that stream; only stream_close finishes the exchange.
func sendExecutionReplies(req *Value, replies []*Value, send bool, sender func(*Value) error) (bool, error) {
	if !send {
		return false, nil
	}
	executed := false
	for _, reply := range replies {
		if reply == nil {
			continue
		}
		if reply.Oneof("message") == "exec_client_message" {
			executed = true
		}
		if err := sender(reply); err != nil {
			return executed, err
		}
	}
	if executed {
		return true, sender(execControl(req, "stream_close", "ExecClientStreamClose"))
	}
	return false, nil
}

// Keep a long local execution alive independently of the inbound stream. Join
// the sender before posting results/close, so no heartbeat can follow close.
func executionHeartbeat(ctx context.Context, req *Value, interval time.Duration, send func(*Value) error, abort context.CancelFunc) func() error {
	if req == nil {
		return func() error { return nil }
	}
	done, joined := make(chan struct{}), make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				joined <- nil
				return
			case <-done:
				joined <- nil
				return
			case <-ticker.C:
				if err := send(execControl(req, "heartbeat", "ExecClientHeartbeat")); err != nil {
					abort()
					joined <- err
					return
				}
			}
		}
	}()
	return func() error { close(done); return <-joined }
}
