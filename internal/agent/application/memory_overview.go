package application

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
)

// memoryOverviewNow is the clock refreshMemoryOverviewIfCold measures idle
// time and overview lag with; tests replace it.
var memoryOverviewNow = time.Now

// memoryOverviewMaxLag bounds how long MEMORY.md may lag behind memory written
// during conversations. Past it, the next turn of any session rewrites
// MEMORY.md even while that session or another one still has a warm prompt
// cache, so a bot whose sessions never all go cold still sees its index catch
// up.
const memoryOverviewMaxLag = 30 * time.Minute

// memoryOverviewActivityHorizon is the longest prompt cache TTL; activity
// older than this can no longer keep any session warm.
const memoryOverviewActivityHorizon = time.Hour

// Reasons a stale MEMORY.md is rewritten before the system prompt is built.
const (
	memoryOverviewRefreshNewSession   = "new_session"
	memoryOverviewRefreshCacheExpired = "cache_expired"
	memoryOverviewRefreshMaxLag       = "max_lag"
)

// refreshMemoryOverviewIfCold rewrites a MEMORY.md that memory written during
// a conversation left behind, but only on a turn whose prompt cache is cold
// anyway: the session's first turn, or its first turn after the provider's
// cache TTL elapsed since the session's last run. MEMORY.md sits in the system
// prompt ahead of the whole history, so rewriting it while the session is warm
// would make every following token of the request miss the cache. MEMORY.md
// is shared by every session of the bot, so a cold turn also waits while
// another session may still be warm. Once MEMORY.md has lagged for
// memoryOverviewMaxLag, the next turn rewrites it regardless. Memory written
// in between still reaches the model through recall and the memory tools,
// which read the memory store rather than MEMORY.md.
//
// It runs before the system files are read and is a no-op unless the bot's
// memory provider deferred an overview rewrite. A subagent session never
// refreshes: it is new by construction while its parent session is warm, and
// the rewrite would break the parent's next request instead.
func (s *Service) refreshMemoryOverviewIfCold(ctx context.Context, botID, sessionID, sessionType, promptCacheTTL string) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || botID == "" || strings.EqualFold(strings.TrimSpace(sessionType), sessionmode.Subagent) {
		return
	}
	refresher, ok := s.resolveMemoryProvider(ctx, botID).(memprovider.OverviewRefresher)
	if !ok {
		return
	}
	now := memoryOverviewNow()
	defer s.memoryOverviewActivity.noteTurn(botID, sessionID, now)

	since, stale := refresher.OverviewStaleSince(botID)
	if !stale {
		return
	}
	// A bot the memory store had not seen starts lagging when first asked,
	// which is a moment after now.
	lag := max(now.Sub(since), 0)
	reason := memoryOverviewRefreshMaxLag
	if lag < memoryOverviewMaxLag {
		ttl := promptCacheTTLDuration(promptCacheTTL)
		var cold bool
		if reason, cold = s.sessionPromptCacheCold(ctx, sessionID, now, ttl); !cold {
			return
		}
		if s.otherSessionWarm(ctx, botID, sessionID, now, ttl) {
			if s.logger != nil {
				s.logger.DebugContext(ctx, "memory overview refresh deferred: another session may be warm",
					slog.String("bot_id", botID),
					slog.String("session_id", sessionID),
					slog.Duration("lag", lag),
				)
			}
			return
		}
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
			slog.Duration("lag", lag),
		)
	}
}

// sessionPromptCacheCold reports whether the session's prompt cache has
// expired: it has no finished run yet, or its last run ended at least ttl ago.
// A lookup failure counts as warm, which keeps the system prompt as it is.
func (s *Service) sessionPromptCacheCold(ctx context.Context, sessionID string, now time.Time, ttl time.Duration) (string, bool) {
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
	if now.Sub(rows[0].CreatedAt.Time) >= ttl {
		return memoryOverviewRefreshCacheExpired, true
	}
	return "", false
}

// otherSessionWarm reports whether another session of the bot may still hold
// a warm prompt cache: it ran in this process within ttl, or it has a run in
// flight. The other sessions are assumed to use a provider that keeps its
// cache as long as this session's does. A failed lookup counts as warm;
// memoryOverviewMaxLag still bounds how long that defers the refresh.
func (s *Service) otherSessionWarm(ctx context.Context, botID, sessionID string, now time.Time, ttl time.Duration) bool {
	if s.memoryOverviewActivity.warmOther(botID, sessionID, now.Add(-ttl)) {
		return true
	}
	if s.queries == nil {
		return false
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return false
	}
	runs, err := s.queries.ListActiveSessionRunsByBot(ctx, botUUID)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "memory overview refresh: load active runs failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
		return true
	}
	self := db.ParseUUIDOrEmpty(sessionID)
	for _, run := range runs {
		if !self.Valid || run.SessionID.Bytes != self.Bytes {
			return true
		}
	}
	return false
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

// sessionActivity remembers when each session of a bot last started a turn or
// finished a run in this process, so a cold turn can tell whether rewriting
// the bot-wide MEMORY.md would break another session's warm prompt cache.
// Subagent sessions are never recorded.
type sessionActivity struct {
	mu   sync.Mutex
	last map[string]map[string]time.Time
}

// noteTurn records that sessionID of botID is starting a turn at now.
func (a *sessionActivity) noteTurn(botID, sessionID string, now time.Time) {
	if sessionID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = make(map[string]map[string]time.Time)
	}
	sessions := a.last[botID]
	if sessions == nil {
		sessions = make(map[string]time.Time)
		a.last[botID] = sessions
	}
	sessions[sessionID] = now
	pruneSessionActivity(sessions, now)
}

// noteRunEnd records that a run of sessionID finished at now. Only sessions
// that started a turn through refreshMemoryOverviewIfCold are tracked, which
// keeps subagent sessions out.
func (a *sessionActivity) noteRunEnd(botID, sessionID string, now time.Time) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, tracked := a.last[botID][sessionID]; tracked {
		a.last[botID][sessionID] = now
	}
}

// warmOther reports whether a session of botID other than sessionID was active
// at or after since.
func (a *sessionActivity) warmOther(botID, sessionID string, since time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for other, at := range a.last[botID] {
		if other != sessionID && !at.Before(since) {
			return true
		}
	}
	return false
}

func pruneSessionActivity(sessions map[string]time.Time, now time.Time) {
	for sessionID, at := range sessions {
		if now.Sub(at) > memoryOverviewActivityHorizon {
			delete(sessions, sessionID)
		}
	}
}
