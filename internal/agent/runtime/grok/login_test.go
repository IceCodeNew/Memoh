package grok

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSignInHandsOutTheDeviceURLWhileGrokWaits(t *testing.T) {
	grok := &fakeGrok{approve: make(chan struct{})}
	s := &signIn{id: "login-1", conn: newTestServer(t, grok).conn, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if start.LoginID != "login-1" || start.UserCode != "ABCD-1234" || start.VerificationURL != "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234" {
		t.Fatalf("begin() = %+v, want the device URL with its code", start)
	}
	select {
	case <-s.done:
		t.Fatal("the sign-in ended before the user confirmed")
	default:
	}

	close(grok.approve)
	select {
	case <-s.done:
	case <-ctx.Done():
		t.Fatal("the sign-in did not end after the user confirmed")
	}
	if s.err != nil || s.meta["email"] != "dev@example.test" {
		t.Fatalf("sign-in ended with err=%v meta=%v", s.err, s.meta)
	}
}

func TestSignInReportsWhatGrokRefused(t *testing.T) {
	grok := &fakeGrok{authErr: errors.New("Authentication required")}
	s := &signIn{id: "login-1", conn: newTestServer(t, grok).conn, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if start, err := s.begin(ctx); err == nil {
		t.Fatalf("begin() = %+v, want Grok's refusal", start)
	}
}

func TestSignInGateSerializesOnlyTheSameAgent(t *testing.T) {
	d := &Driver{running: map[string]*agentProcesses{}}
	release, err := d.lockSignIn(t.Context(), "bot", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.lockSignIn(ctx, "bot", "agent-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("second start entered held gate: %v", err)
	}
	other, err := d.lockSignIn(t.Context(), "bot", "agent-b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	again, err := d.lockSignIn(t.Context(), "bot", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestReplacingSignInCancelsOnlyThatAgentsPreviousAttempts(t *testing.T) {
	canceled := 0
	old := &signIn{id: "old", botID: "bot", botAgentID: "agent-a", cancel: func() { canceled++ }}
	other := &signIn{id: "other", botID: "bot", botAgentID: "agent-b", cancel: func() { t.Error("other Agent canceled") }}
	d := &Driver{signIns: map[string]*signIn{old.id: old, other.id: other}}
	d.cancelAgentSignIns(t.Context(), "bot", "agent-a")
	if canceled != 1 || d.signIn("old", "agent-a") != nil || d.signIn("other", "agent-b") != other {
		t.Fatal("replacement did not isolate and remove old login")
	}
}

func TestReplacingSignInKeepsALoginTheUserApproved(t *testing.T) {
	ended := func(id string, err error, cancel func()) *signIn {
		s := &signIn{id: id, botID: "bot", botAgentID: "agent", done: make(chan struct{}), err: err, cancel: cancel}
		close(s.done)
		return s
	}
	approved := ended("approved", nil, func() { t.Error("approved login canceled before its poll stored it") })
	refusedCanceled := false
	refused := ended("refused", errors.New("denied"), func() { refusedCanceled = true })
	d := &Driver{signIns: map[string]*signIn{approved.id: approved, refused.id: refused}}
	d.cancelAgentSignIns(t.Context(), "bot", "agent")
	if d.signIn("approved", "agent") != approved || d.signIn("refused", "agent") != nil || !refusedCanceled {
		t.Fatal("replacement must end only sign-ins that cannot complete")
	}
}
