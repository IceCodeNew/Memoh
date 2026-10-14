package hooks

import (
	"context"
	"sync"
)

type turnSnapshotKey struct{}

type turnSnapshot struct {
	mu      sync.Mutex
	entries map[string]*snapshotEntry
}

type snapshotEntry struct {
	once   sync.Once
	cfg    Config
	exists bool
	err    error
}

// WithTurnSnapshot scopes hooks configuration loading to one chat turn: the
// first load for a bot and workspace target is reused by every later hook
// event of the turn, including a load failure. It is idempotent.
func WithTurnSnapshot(ctx context.Context) context.Context {
	if turnSnapshotFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, turnSnapshotKey{}, &turnSnapshot{entries: map[string]*snapshotEntry{}})
}

func turnSnapshotFrom(ctx context.Context) *turnSnapshot {
	if ctx == nil {
		return nil
	}
	snapshot, _ := ctx.Value(turnSnapshotKey{}).(*turnSnapshot)
	return snapshot
}

func (t *turnSnapshot) load(key string, load func() (Config, bool, error)) (Config, bool, error) {
	t.mu.Lock()
	entry := t.entries[key]
	if entry == nil {
		entry = &snapshotEntry{}
		t.entries[key] = entry
	}
	t.mu.Unlock()
	entry.once.Do(func() {
		entry.cfg, entry.exists, entry.err = load()
	})
	return entry.cfg.clone(), entry.exists, entry.err
}

// clone copies what running a configuration mutates (applyDefaults fills in
// hook and action fields), so concurrent hook events of a turn never share it.
func (c Config) clone() Config {
	c.Env = cloneStringMap(c.Env)
	if c.Hooks != nil {
		hooks := make([]Hook, len(c.Hooks))
		for i, hook := range c.Hooks {
			if hook.Actions != nil {
				hook.Actions = append([]HookAction(nil), hook.Actions...)
			}
			hooks[i] = hook
		}
		c.Hooks = hooks
	}
	return c
}
