package application

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/hooks"
)

// Only a session that answers through its tools is refused when they are
// withheld; a chat turn continues as text, and a turn with its tools runs.
func TestRequireToolsForRefusesOnlySessionsThatAnswerThroughTools(t *testing.T) {
	workspaceDown := native.RunConfig{ToolsWithheld: native.ToolsWithheldWorkspaceUnavailable}
	hooksDown := native.RunConfig{ToolsWithheld: native.ToolsWithheldHooksUnavailable}
	for _, sessionType := range []string{sessionmode.Discuss, sessionmode.Schedule, " Discuss "} {
		if err := requireToolsFor(sessionType, workspaceDown); apperror.CodeOf(err) != apperror.CodeWorkspaceUnreachable {
			t.Errorf("%q, workspace unavailable: error = %v, want %s", sessionType, err, apperror.CodeWorkspaceUnreachable)
		}
		if err := requireToolsFor(sessionType, hooksDown); !errors.Is(err, hooks.ErrConfigUnavailable) {
			t.Errorf("%q, hooks unavailable: error = %v, want ErrConfigUnavailable", sessionType, err)
		}
		if err := requireToolsFor(sessionType, native.RunConfig{SupportsToolCall: true}); err != nil {
			t.Errorf("%q with its tools: error = %v", sessionType, err)
		}
	}
	for _, sessionType := range []string{sessionmode.Chat, "", sessionmode.ACPAgent} {
		if err := requireToolsFor(sessionType, hooksDown); err != nil {
			t.Errorf("%q: error = %v, want the text-only turn to continue", sessionType, err)
		}
	}
}

// A scheduled run's plain text is logged only, so a run that would have to
// withhold its tools is refused before the model is called, and the run is
// recorded as failed instead of reporting success.
func TestTriggerScheduleWithUnavailableHooksConfigIsRefused(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.hookService = hooks.NewService(nil, newBrokenHooksProvider(t))

	_, err := triggerDirectSchedule(t, fixture.service)
	if !errors.Is(err, hooks.ErrConfigUnavailable) {
		t.Fatalf("TriggerSchedule() error = %v, want ErrConfigUnavailable", err)
	}
	if requests := fixture.requests.all(); len(requests) != 0 {
		t.Fatalf("provider received %d requests, want none", len(requests))
	}
	if len(fixture.runtime.finishes) != 1 || fixture.runtime.finishes[0].status != sessionruntime.RunStatusErrored {
		t.Fatalf("runtime finishes = %#v, want one errored finish", fixture.runtime.finishes)
	}
}
