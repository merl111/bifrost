package ui

import (
	"bifrost/internal/execpolicy"
	"context"
	"strings"
	"testing"
)

func TestExecutionApprovalRequiresExplicitKey(t *testing.T) {
	m := New(t.TempDir())
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &execpolicy.Request{Argv: []string{"sh", "-c", "touch marker"}, Root: "/fixture", Reply: make(chan bool, 1), Done: ctx.Done()}
	m.appendChatEvent(ChatEvent{Kind: "approval", Approval: r})
	if !strings.Contains(m.render(), "Command needs approval") {
		t.Fatal("no prompt")
	}
	if !m.handleExecutionApproval("y") {
		t.Fatal("normal input escaped modal")
	}
	select {
	case <-r.Reply:
		t.Fatal("plain y approved")
	default:
	}
	m.handleExecutionApproval("ctrl+y")
	if !<-r.Reply {
		t.Fatal("not approved")
	}
	m.appendChatEvent(ChatEvent{Kind: "approval", Approval: r})
	cancel()
	if m.pendingExecutionApproval() != nil {
		t.Fatal("stale request remains")
	}
}
