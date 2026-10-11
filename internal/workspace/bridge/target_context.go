package bridge

import (
	"context"
	"strings"
	"time"
)

// AuxiliaryLoadTimeout bounds the best-effort workspace reads made while
// preparing a chat turn: the target probe, skills, hooks and context files.
// Explicit failures degrade immediately; this only catches a workspace that
// stops responding, so it stays well above a slow but healthy bridge.
const AuxiliaryLoadTimeout = 10 * time.Second

type workspaceTargetContextKey struct{}

func WithWorkspaceTarget(ctx context.Context, targetID string) context.Context {
	return context.WithValue(ctx, workspaceTargetContextKey{}, strings.TrimSpace(targetID))
}

func WorkspaceTargetFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	targetID, _ := ctx.Value(workspaceTargetContextKey{}).(string)
	return strings.TrimSpace(targetID)
}

type workspaceUnavailableContextKey struct{}

// WithWorkspaceUnavailable fences workspace access for a pure conversation
// turn without changing its selected target.
func WithWorkspaceUnavailable(ctx context.Context) context.Context {
	return context.WithValue(ctx, workspaceUnavailableContextKey{}, true)
}

func WorkspaceUnavailableFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	unavailable, _ := ctx.Value(workspaceUnavailableContextKey{}).(bool)
	return unavailable
}
