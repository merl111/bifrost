package execpolicy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApprovalModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		argv []string
		asks int
	}{{"allow", []string{"sh", "-c", "anything"}, 0}, {"ask", []string{"pwd"}, 1}, {"rules", []string{"pwd"}, 0}, {"rules", []string{"sh", "-c", "pwd; touch marker"}, 1}, {"rules", []string{"ls", "-la", "extra"}, 1}} {
		t.Run(tc.mode+tc.argv[0], func(t *testing.T) {
			asked := 0
			authorize := (Settings{Mode: tc.mode}).Authorizer(func(r *Request) { asked++; r.Reply <- true })
			if err := authorize(context.Background(), tc.argv, "/project", ""); err != nil {
				t.Fatal(err)
			}
			if asked != tc.asks {
				t.Fatalf("asked %d", asked)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	authorize := (Settings{Mode: "ask"}).Authorizer(func(r *Request) { cancel(); r.Reply <- true })
	if err := authorize(ctx, []string{"pwd"}, "/project", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled approval: %v", err)
	}
	if err := (Settings{Mode: "ask"}).Authorizer(nil)(context.Background(), []string{"pwd"}, "", ""); err == nil {
		t.Fatal("headless approval bypass")
	}
	if err := (Settings{Mode: "invalid"}).Validate(); err == nil {
		t.Fatal("invalid mode")
	}
}
func TestApprovalDenialAndCancellation(t *testing.T) {
	if err := (Settings{Mode: "ask"}).Authorizer(func(r *Request) { r.Reply <- false })(context.Background(), []string{"pwd"}, "", ""); err == nil {
		t.Fatal("denial ignored")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := (Settings{Mode: "ask"}).Authorizer(func(*Request) {})(ctx, []string{"pwd"}, "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestDetachedApprovalExpiresWithTurn(t *testing.T) {
	turn, cancel := context.WithCancel(context.Background())
	authorize := WithLifetime(turn, Settings{Mode: "ask"}.Authorizer(func(r *Request) { cancel(); r.Reply <- true }))
	if err := authorize(context.Background(), []string{"sh", "-c", "ls"}, "/project", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("detached approval: %v", err)
	}
}
