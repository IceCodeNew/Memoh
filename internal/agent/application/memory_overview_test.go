package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/storefs"
	"github.com/felinics/memoh/internal/settings"
)

const overviewTestSessionID = "33333333-3333-3333-3333-333333333333"

type overviewRefreshProvider struct {
	memprovider.Provider
	stale      bool
	refreshErr error
	refreshes  int
	afterChat  chan context.Context
}

func (*overviewRefreshProvider) Type() string { return "test" }

func (p *overviewRefreshProvider) OverviewStale(string) bool { return p.stale }

func (p *overviewRefreshProvider) RefreshOverview(context.Context, string) error {
	p.refreshes++
	if p.refreshErr != nil {
		return p.refreshErr
	}
	p.stale = false
	return nil
}

func (p *overviewRefreshProvider) OnAfterChat(ctx context.Context, _ memprovider.AfterChatRequest) error {
	p.afterChat <- ctx
	return nil
}

type lastRunQueries struct {
	storeRoundSettingsQueries
	lastRun time.Time
	err     error
	calls   int
}

func (q *lastRunQueries) ListRecentContextLifecyclesBySession(_ context.Context, arg sqlc.ListRecentContextLifecyclesBySessionParams) ([]sqlc.ListRecentContextLifecyclesBySessionRow, error) {
	q.calls++
	if arg.MaxCount != 1 {
		return nil, errors.New("unexpected max_count")
	}
	if q.err != nil {
		return nil, q.err
	}
	if q.lastRun.IsZero() {
		return nil, nil
	}
	return []sqlc.ListRecentContextLifecyclesBySessionRow{{CreatedAt: pgtype.Timestamptz{Time: q.lastRun, Valid: true}}}, nil
}

func overviewRefreshService(provider memprovider.Provider, queries dbstore.Queries) *Service {
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, provider)
	return &Service{
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		queries:         queries,
		logger:          slog.New(slog.DiscardHandler),
	}
}

func TestWriteMemoryMarksPostTurnWritesAsConversationWrites(t *testing.T) {
	t.Parallel()

	provider := &overviewRefreshProvider{afterChat: make(chan context.Context, 1)}
	service := overviewRefreshService(provider, nil)
	service.messageService = &recordingMessageService{}

	if err := service.writeMemory(context.Background(), ChatRequest{BotID: storeRoundBotID, ThreadID: "session-1"}, storeRoundBotID, []messagepkg.Message{
		storedMemoryTestMessage(t, "msg-assistant", "session-1", "assistant", "stored answer"),
	}); err != nil {
		t.Fatalf("writeMemory: %v", err)
	}
	if ctx := <-provider.afterChat; !storefs.IsConversationWrite(ctx) {
		t.Fatal("post-turn memory write is not marked as a conversation write")
	}
}

func TestRefreshMemoryOverviewIfCold(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	previous := memoryOverviewNow
	memoryOverviewNow = func() time.Time { return now }
	t.Cleanup(func() { memoryOverviewNow = previous })

	cases := []struct {
		name          string
		stale         bool
		lastRun       time.Time
		lookupErr     error
		ttl           string
		sessionID     string
		sessionType   string
		wantRefreshes int
		wantLookups   int
	}{
		{name: "fresh overview skips the lookup", stale: false, lastRun: now.Add(-time.Hour), sessionID: overviewTestSessionID},
		{name: "new session refreshes", stale: true, sessionID: overviewTestSessionID, wantRefreshes: 1, wantLookups: 1},
		{name: "warm session keeps the overview", stale: true, lastRun: now.Add(-time.Minute), sessionID: overviewTestSessionID, wantLookups: 1},
		{name: "expired default cache refreshes", stale: true, lastRun: now.Add(-6 * time.Minute), sessionID: overviewTestSessionID, wantRefreshes: 1, wantLookups: 1},
		{name: "one hour cache stays warm after six minutes", stale: true, lastRun: now.Add(-6 * time.Minute), ttl: "1h", sessionID: overviewTestSessionID, wantLookups: 1},
		{name: "one hour cache refreshes after an hour", stale: true, lastRun: now.Add(-61 * time.Minute), ttl: "1h", sessionID: overviewTestSessionID, wantRefreshes: 1, wantLookups: 1},
		{name: "cache off still waits for the default ttl", stale: true, lastRun: now.Add(-time.Minute), ttl: "off", sessionID: overviewTestSessionID, wantLookups: 1},
		{name: "lookup failure keeps the overview", stale: true, lookupErr: errors.New("db down"), sessionID: overviewTestSessionID, wantLookups: 1},
		{name: "sessionless run refreshes", stale: true, sessionID: "", wantRefreshes: 1},
		{name: "subagent session never refreshes", stale: true, sessionID: overviewTestSessionID, sessionType: "subagent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &overviewRefreshProvider{stale: tc.stale}
			queries := &lastRunQueries{lastRun: tc.lastRun, err: tc.lookupErr}
			service := overviewRefreshService(provider, queries)

			service.refreshMemoryOverviewIfCold(context.Background(), storeRoundBotID, tc.sessionID, tc.sessionType, tc.ttl)

			if provider.refreshes != tc.wantRefreshes {
				t.Fatalf("refreshes = %d, want %d", provider.refreshes, tc.wantRefreshes)
			}
			if queries.calls != tc.wantLookups {
				t.Fatalf("last-run lookups = %d, want %d", queries.calls, tc.wantLookups)
			}
		})
	}
}

func TestRefreshMemoryOverviewIfColdSurvivesRefreshFailure(t *testing.T) {
	t.Parallel()

	provider := &overviewRefreshProvider{stale: true, refreshErr: errors.New("workspace unavailable")}
	service := overviewRefreshService(provider, &lastRunQueries{})

	service.refreshMemoryOverviewIfCold(context.Background(), storeRoundBotID, overviewTestSessionID, "", "")

	if provider.refreshes != 1 || !provider.stale {
		t.Fatalf("refreshes = %d stale = %v, want one failed attempt that leaves the overview stale", provider.refreshes, provider.stale)
	}
}

func TestRefreshMemoryOverviewIfColdIgnoresProvidersWithoutRefresher(t *testing.T) {
	t.Parallel()

	queries := &lastRunQueries{}
	service := overviewRefreshService(&storeRoundMemoryProvider{}, queries)

	service.refreshMemoryOverviewIfCold(context.Background(), storeRoundBotID, overviewTestSessionID, "", "")

	if queries.calls != 0 {
		t.Fatalf("last-run lookups = %d, want none for a provider that never defers MEMORY.md", queries.calls)
	}
}
