package decision

import (
	"context"
	"log/slog"
	"strings"
)

// WorkspaceTargetMapper translates between the client-facing workspace target
// id carried by requests ("native" for the bot's managed computer, the
// binding id for a remote mount, empty for the bot's primary target) and the
// workspace id the decision tables store. workspace.Manager implements it
// from stored state alone; no runtime is dialed.
type WorkspaceTargetMapper interface {
	WorkspaceIDForTarget(ctx context.Context, botID, targetID string) (string, error)
	TargetForWorkspace(ctx context.Context, botID, workspaceID string) (targetID, kind string, err error)
}

// WorkspaceIDForTarget resolves the workspace a request's target names. A
// nil mapper (focused tests, or a server without workspace records) stores
// no workspace.
func WorkspaceIDForTarget(ctx context.Context, mapper WorkspaceTargetMapper, botID, targetID string) (string, error) {
	if mapper == nil {
		return "", nil
	}
	return mapper.WorkspaceIDForTarget(ctx, botID, strings.TrimSpace(targetID))
}

// TargetAddress maps a stored workspace id back to the client-facing target
// id. A workspace that no longer resolves to one of the bot's targets keeps
// its id as the address: resolving it later fails loudly instead of silently
// running on the primary target.
func TargetAddress(ctx context.Context, mapper WorkspaceTargetMapper, logger *slog.Logger, botID, workspaceID string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || mapper == nil {
		return workspaceID
	}
	targetID, _, err := mapper.TargetForWorkspace(ctx, botID, workspaceID)
	if err != nil {
		if logger != nil {
			logger.DebugContext(ctx, "workspace target no longer resolves",
				slog.String("bot_id", botID), slog.String("workspace_id", workspaceID), slog.Any("error", err))
		}
		return workspaceID
	}
	return targetID
}
