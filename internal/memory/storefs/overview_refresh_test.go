package storefs

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

var errWorkspaceUnavailable = errors.New("workspace unavailable")

// recordingWorkspace fails every workspace access, so a test can tell whether
// the Service tried to touch MEMORY.md and with which context.
type recordingWorkspace struct {
	calls int
	ctxs  []context.Context
}

func (w *recordingWorkspace) MCPClient(ctx context.Context, _ string) (*bridge.Client, error) {
	w.calls++
	w.ctxs = append(w.ctxs, ctx)
	return nil, errWorkspaceUnavailable
}

func TestConversationWriteDefersOverviewInCacheColdMode(t *testing.T) {
	t.Parallel()

	workspace := &recordingWorkspace{}
	s := New(nil, workspace, WithOverviewRefresh(config.MemoryOverviewRefreshCacheCold))

	if err := s.SyncOverview(WithConversationWrite(context.Background()), "bot-1"); err != nil {
		t.Fatalf("SyncOverview: %v", err)
	}
	if workspace.calls != 0 {
		t.Fatalf("workspace calls = %d, want the MEMORY.md rewrite deferred", workspace.calls)
	}
	if !s.OverviewStale("bot-1") {
		t.Fatal("deferred rewrite did not mark the overview stale")
	}
}

func TestImmediateModeRewritesOverviewForConversationWrite(t *testing.T) {
	t.Parallel()

	workspace := &recordingWorkspace{}
	s := New(nil, workspace)

	if err := s.SyncOverview(WithConversationWrite(context.Background()), "bot-1"); !errors.Is(err, errWorkspaceUnavailable) {
		t.Fatalf("SyncOverview error = %v, want the rewrite attempted", err)
	}
	if s.OverviewStale("bot-1") {
		t.Fatal("immediate mode never reports a stale overview")
	}
}

func TestUnmarkedWriteRewritesOverviewInCacheColdMode(t *testing.T) {
	t.Parallel()

	workspace := &recordingWorkspace{}
	s := New(nil, workspace, WithOverviewRefresh(config.MemoryOverviewRefreshCacheCold))

	if err := s.SyncOverview(context.Background(), "bot-1"); !errors.Is(err, errWorkspaceUnavailable) {
		t.Fatalf("SyncOverview error = %v, want a management write rewritten immediately", err)
	}
}

func TestRefreshOverviewRewritesStaleOverviewWithoutConversationMark(t *testing.T) {
	t.Parallel()

	workspace := &recordingWorkspace{}
	s := New(nil, workspace, WithOverviewRefresh(config.MemoryOverviewRefreshCacheCold))
	if !s.OverviewStale("bot-1") {
		t.Fatal("a bot this process never wrote must count as stale")
	}

	err := s.RefreshOverview(WithConversationWrite(context.Background()), "bot-1")
	if !errors.Is(err, errWorkspaceUnavailable) {
		t.Fatalf("RefreshOverview error = %v, want the rewrite attempted", err)
	}
	if len(workspace.ctxs) == 0 || IsConversationWrite(workspace.ctxs[0]) {
		t.Fatal("refresh must write MEMORY.md, not defer it again")
	}
	if !s.OverviewStale("bot-1") {
		t.Fatal("a failed refresh must leave the overview stale")
	}
}

func TestOverviewCleanIgnoresRefreshSupersededByNewerWrite(t *testing.T) {
	t.Parallel()

	workspace := &recordingWorkspace{}
	s := New(nil, workspace, WithOverviewRefresh(config.MemoryOverviewRefreshCacheCold))
	conversation := WithConversationWrite(context.Background())

	generation := s.overviewGeneration("bot-1")
	if !s.deferOverview(conversation, "bot-1") {
		t.Fatal("conversation write was not deferred")
	}
	s.markOverviewClean("bot-1", generation)
	if !s.OverviewStale("bot-1") {
		t.Fatal("a refresh that read the files before a newer write must not mark the overview clean")
	}

	s.markOverviewClean("bot-1", s.overviewGeneration("bot-1"))
	if s.OverviewStale("bot-1") {
		t.Fatal("overview should be clean after a refresh that saw every write")
	}
	if err := s.RefreshOverview(context.Background(), "bot-1"); err != nil {
		t.Fatalf("RefreshOverview on a clean overview: %v", err)
	}
	if workspace.calls != 0 {
		t.Fatalf("workspace calls = %d, want none for a clean overview", workspace.calls)
	}
}
