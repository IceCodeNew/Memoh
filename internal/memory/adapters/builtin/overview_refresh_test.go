package builtin

import (
	"context"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

// refreshingStore is a memory store that defers MEMORY.md rewrites.
type refreshingStore struct {
	*fakeStore
	stale     bool
	refreshes int
}

func (s *refreshingStore) OverviewStale(string) bool { return s.stale }

func (s *refreshingStore) RefreshOverview(context.Context, string) error {
	s.refreshes++
	s.stale = false
	return nil
}

func TestBuiltinProviderRefreshesOverviewThroughGraphRuntime(t *testing.T) {
	t.Parallel()

	store := &refreshingStore{fakeStore: newFakeStore(), stale: true}
	provider := NewBuiltinProvider(nil, NewGraphRuntime(nil, nil, store))
	var refresher adapters.OverviewRefresher = provider

	if !refresher.OverviewStale("bot-1") {
		t.Fatal("provider did not report the store's stale overview")
	}
	if err := refresher.RefreshOverview(context.Background(), "bot-1"); err != nil {
		t.Fatalf("RefreshOverview: %v", err)
	}
	if store.refreshes != 1 || refresher.OverviewStale("bot-1") {
		t.Fatalf("refreshes = %d stale = %v, want one refresh that clears the stale overview", store.refreshes, refresher.OverviewStale("bot-1"))
	}
}

func TestBuiltinProviderWithoutDeferringStoreNeverReportsStaleOverview(t *testing.T) {
	t.Parallel()

	provider := NewBuiltinProvider(nil, NewGraphRuntime(nil, nil, newFakeStore()))

	if provider.OverviewStale("bot-1") {
		t.Fatal("a store that rewrites MEMORY.md on every write never lags behind")
	}
	if err := provider.RefreshOverview(context.Background(), "bot-1"); err != nil {
		t.Fatalf("RefreshOverview: %v", err)
	}
}
