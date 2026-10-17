package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTrustedExplicitArgvHook(t *testing.T) {
	r, err := New(Config{Trusted: true, Hooks: []Hook{{
		Name: "fixture", Event: BeforeTool,
		Command: []string{"sh", "-c", `printf '%s' "$BIFROST_HOOK_EVENT:$BIFROST_HOOK_INPUT"`},
		Timeout: time.Second, MaxOutputBytes: 1024,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), BeforeTool, map[string]string{"x": "y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || !strings.Contains(string(result[0].Process.Stdout), "before-tool") || !strings.Contains(string(result[0].Process.Stdout), `"x":"y"`) {
		t.Fatalf("result=%+v", result)
	}
}

func TestHooksRequireTrustAndRejectUnknownEvent(t *testing.T) {
	if _, err := New(Config{Hooks: []Hook{{Name: "x", Event: BeforeTurn, Command: []string{"true"}}}}); err == nil {
		t.Fatal("untrusted hooks accepted")
	}
	if _, err := New(Config{Trusted: true, Hooks: []Hook{{Name: "x", Event: "recursive", Command: []string{"true"}}}}); err == nil {
		t.Fatal("unknown event accepted")
	}
}

func TestHookTimeoutAndRecursionGuard(t *testing.T) {
	r, err := New(Config{Trusted: true, Hooks: []Hook{{Name: "slow", Event: AfterTurn, Command: []string{"sh", "-c", "sleep 5"}, Timeout: 20 * time.Millisecond}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), AfterTurn, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Run(ctx, AfterTurn, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("guard was not released: %v", err)
	}
}
