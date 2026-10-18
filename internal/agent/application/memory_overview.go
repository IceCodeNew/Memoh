package application

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
)

// memoryOverviewNow is the clock refreshMemoryOverviewIfCold measures the
// session's idle time with; tests replace it.
var memoryOverviewNow = time.Now

// Reasons a stale MEMORY.md is rewritten before the system prompt is built.
const (
	memoryOverviewRefreshNewSession   = "new_session"
	memoryOverviewRefreshCacheExpired = "cache_expired"
)

// refreshMemoryOverviewIfCold rewrites a MEMORY.md that memory written during
// a conversation left behind, but only on a turn whose prompt cache is cold
// anyway: the session's first turn, or its first turn after the provider's
// cache TTL elapsed since the session's last run. MEMORY.md sits in the system
// prompt ahead of the whole history, so rewriting it while the session is warm
// would make every following token of the request miss the cache. Memory a
// warm session wrote still reaches the model through recall and the memory
// tools, which read the memory store rather than MEMORY.md.
//
// It runs before the system files are read and is a no-op unless the bot's
// memory provider deferred an overview rewrite (agent.memory_overview_refresh
// = "cache_cold"). A subagent session never refreshes: it is new by
// construction while its parent session is warm, and the rewrite would break
// the parent's next request instead.
func (s *Service) refreshMemoryOverviewIfCold(ctx context.Context, botID, sessionID, sessionType, promptCacheTTL string) {
	botID = strings.TrimSpace(botID)
	if s == nil || botID == "" || strings.EqualFold(strings.TrimSpace(sessionType), sessionmode.Subagent) {
		return
	}
	refresher, ok := s.resolveMemoryProvider(ctx, botID).(memprovider.OverviewRefresher)
	if !ok || !refresher.OverviewStale(botID) {
		return
	}
	reason, cold := s.sessionPromptCacheCold(ctx, sessionID, promptCacheTTL)
	if !cold {
		return
	}
	if err := refresher.RefreshOverview(ctx, botID); err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "memory overview refresh failed",
				slog.String("bot_id", botID),
				slog.String("session_id", sessionID),
				slog.String("reason", reason),
				slog.Any("error", err),
			)
		}
		return
	}
	if s.logger != nil {
		s.logger.DebugContext(ctx, "memory overview refreshed",
			slog.String("bot_id", botID),
			slog.String("session_id", sessionID),
			slog.String("reason", reason),
		)
	}
}

// sessionPromptCacheCold reports whether the session's prompt cache has
// expired: it has no finished run yet, or its last run ended at least one
// cache TTL ago. A lookup failure counts as warm, which keeps the system
// prompt as it is.
func (s *Service) sessionPromptCacheCold(ctx context.Context, sessionID, promptCacheTTL string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s.queries == nil {
		return memoryOverviewRefreshNewSession, true
	}
	sessionUUID, err := db.ParseUUID(sessionID)
	if err != nil {
		return "", false
	}
	rows, err := s.queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
		SessionID: sessionUUID,
		MaxCount:  1,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "memory overview refresh: load last session run failed",
				slog.String("session_id", sessionID),
				slog.Any("error", err),
			)
		}
		return "", false
	}
	if len(rows) == 0 || !rows[0].CreatedAt.Valid {
		return memoryOverviewRefreshNewSession, true
	}
	if memoryOverviewNow().Sub(rows[0].CreatedAt.Time) >= promptCacheTTLDuration(promptCacheTTL) {
		return memoryOverviewRefreshCacheExpired, true
	}
	return "", false
}

// promptCacheTTLDuration is how long a provider is assumed to keep a prompt
// cache entry. A provider with prompt caching off still gets the default:
// providers with automatic prefix caching keep caching without Memoh's hints.
func promptCacheTTLDuration(ttl string) time.Duration {
	if models.NormalizePromptCacheTTL(strings.TrimSpace(ttl)) == models.PromptCacheTTL1h {
		return time.Hour
	}
	return 5 * time.Minute
}
