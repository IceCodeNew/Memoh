package workspace

import (
	"testing"
	"time"

	ctr "github.com/felinics/memoh/internal/container"
)

func TestInstanceKeyFromContainerID(t *testing.T) {
	t.Parallel()

	const id = "00000000-0000-0000-0000-0000000000c1"
	for _, tc := range []struct {
		name        string
		containerID string
		want        string
		ok          bool
	}{
		{name: "workspace prefix", containerID: "workspace-" + id, want: id, ok: true},
		{name: "legacy mcp prefix", containerID: "mcp-" + id, want: id, ok: true},
		{name: "surrounding space", containerID: "  workspace-" + id + " ", want: id, ok: true},
		{name: "bare id", containerID: id},
		{name: "prefix only", containerID: "workspace-"},
		{name: "empty", containerID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := InstanceKeyFromContainerID(tc.containerID)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("InstanceKeyFromContainerID(%q) = %q, %v; want %q, %v", tc.containerID, got, ok, tc.want, tc.ok)
			}
		})
	}

	// An id without a known prefix is its own key, so a runtime that names
	// containers freely still gets a stable socket directory.
	if got := instanceKey("custom-container"); got != "custom-container" {
		t.Fatalf("instanceKey(bare) = %q", got)
	}
	if got := instanceKey("workspace-" + id); got != id {
		t.Fatalf("instanceKey(workspace-) = %q, want %q", got, id)
	}
}

func TestWorkspaceIDFromContainerInfoOnlyTrustsTheLabel(t *testing.T) {
	t.Parallel()

	const id = "00000000-0000-0000-0000-0000000000c1"
	if got, ok := WorkspaceIDFromContainerInfo(ctr.ContainerInfo{
		ID:     "workspace-ignored",
		Labels: map[string]string{WorkspaceIDLabelKey: id, BotLabelKey: "bot"},
	}); !ok || got != id {
		t.Fatalf("labelled container = %q, %v; want %q", got, ok, id)
	}
	// A workspace-<key> name carries an instance key, which on older
	// containers is a bot id: it does not identify the computer.
	if got, ok := WorkspaceIDFromContainerInfo(ctr.ContainerInfo{ID: "workspace-" + id}); ok {
		t.Fatalf("name-only container resolved to workspace %q", got)
	}
}

func TestBotIDFromContainerInfoPrefersCurrentLabel(t *testing.T) {
	t.Parallel()

	info := ctr.ContainerInfo{
		ID: "workspace-ignored",
		Labels: map[string]string{
			BotLabelKey: "bot-from-label",
		},
		UpdatedAt: time.Now(),
	}

	botID, ok := BotIDFromContainerInfo(info)
	if !ok {
		t.Fatal("expected bot ID to resolve")
	}
	if botID != "bot-from-label" {
		t.Fatalf("expected labeled bot ID, got %q", botID)
	}
}

func TestBotIDFromContainerInfoFallsBackToKnownPrefixes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		containerID string
		want        string
	}{
		{name: "workspace", containerID: "workspace-bot-123", want: "bot-123"},
		{name: "legacy", containerID: "mcp-bot-456", want: "bot-456"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := ctr.ContainerInfo{ID: tc.containerID}
			got, ok := BotIDFromContainerInfo(info)
			if !ok {
				t.Fatal("expected bot ID to resolve")
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
