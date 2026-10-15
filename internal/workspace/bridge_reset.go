package workspace

import (
	"context"
	"slices"
)

// OnBridgeReset registers fn to run each time the bridge connection of a
// managed computer is evicted from the pool — after a container restart,
// rebuild, snapshot replace, or a failed readiness probe. The callback
// receives the id of a bot bound to that computer, once per bound bot, and
// runs synchronously on the resetting goroutine, so it must be cheap and must
// not call back into the Manager's bridge path. Use it to invalidate per-bot
// state derived from the workspace container process. A nil fn is ignored.
func (m *Manager) OnBridgeReset(fn func(botID string)) {
	if fn == nil {
		return
	}
	m.bridgeResetMu.Lock()
	m.bridgeResetFns = append(m.bridgeResetFns, fn)
	m.bridgeResetMu.Unlock()
}

// resetBridge evicts the pooled bridge client of the container and then
// notifies OnBridgeReset subscribers, in registration order, for every bot
// bound to the computer. It is the single invalidation point for anything
// cached against a workspace container process; every code path that used to
// call grpcPool.Remove directly goes through here.
func (m *Manager) resetBridge(ctx context.Context, workspaceID, containerID string) {
	m.grpcPool.Remove(containerID)

	m.bridgeResetMu.Lock()
	fns := slices.Clone(m.bridgeResetFns)
	m.bridgeResetMu.Unlock()
	if len(fns) == 0 {
		return
	}
	for _, botID := range m.boundBots(ctx, workspaceID) {
		for _, fn := range fns {
			fn(botID)
		}
	}
}
