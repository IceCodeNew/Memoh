//go:build integration

package workspacedeps

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

var (
	depsMigrationOnce sync.Once
	depsMigrationErr  error
)

// openDependencyPostgres mirrors the other Postgres integration harnesses:
// TEST_POSTGRES_DSN selects the database, TEST_POSTGRES_BOOTSTRAP_SCHEMA=1
// runs the embedded migration chain first, and the pool binds the default
// team GUC exactly like production.
func openDependencyPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		depsMigrationOnce.Do(func() { depsMigrationErr = dbtest.MigratePostgresUp(dsn) })
		if depsMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", depsMigrationErr)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// createDependencyComputer seeds a user, its team membership, a bot, the
// bot's managed workspace (with an id of its own, as for a bot created after
// 0167), the binding and the managed_computers row the records reference.
// It returns the workspace id. Deleting the workspace cascades to the
// records; the bot and user go with it in cleanup.
func createDependencyComputer(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	uid := uuid.New()
	bid := uuid.New()
	wid := uuid.New()
	name := "workspace-deps-it-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active, metadata)
			VALUES ($1, $2, true, '{}'::jsonb)
			RETURNING id
		), created_member AS (
			INSERT INTO team_members (team_id, user_id, role)
			SELECT $3, id, 'admin' FROM created_user
			RETURNING user_id
		), created_bot AS (
			INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
			SELECT $4, $3, user_id, $2, 'ready', '{}'::jsonb
			FROM created_member
			RETURNING id, owner_user_id
		), created_workspace AS (
			INSERT INTO workspaces (id, team_id, owner_user_id, kind, name)
			SELECT $5, $3, owner_user_id, 'managed', $2
			FROM created_bot
			RETURNING id
		), created_binding AS (
			INSERT INTO bot_workspace_bindings (team_id, bot_id, workspace_id, is_primary)
			SELECT $3, $4, id, true FROM created_workspace
			RETURNING workspace_id
		)
		INSERT INTO managed_computers (workspace_id, team_id, desired_state)
		SELECT workspace_id, $3, 'present' FROM created_binding`,
		uid, name, team.DefaultTeamID, bid, wid,
	); err != nil {
		t.Fatalf("create dependency fixture: %v", err)
	}
	// The test context is already cancelled when cleanup runs.
	t.Cleanup(func() { //nolint:contextcheck // cleanup outlives the test context
		_, _ = pool.Exec(context.Background(), "DELETE FROM bots WHERE id = $1", bid)
		_, _ = pool.Exec(context.Background(), "DELETE FROM workspaces WHERE id = $1", wid)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", uid)
	})
	return wid.String()
}

func newIntegrationStore(pool *pgxpool.Pool) Store {
	return NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool)))
}

func TestPostgresStoreLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDependencyPostgres(t, ctx)
	workspaceID := createDependencyComputer(t, ctx, pool)
	store := newIntegrationStore(pool)
	key := InstallationKey{WorkspaceID: workspaceID, DependencyID: "codex"}

	// Upsert creates the intent row.
	created, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey:  key,
		Source:           InstallationSourceManaged,
		Status:           StatusInstalling,
		InstalledVersion: "",
		ManifestDigest:   "sha256:one",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created.ID == "" || created.WorkspaceID != workspaceID || created.Status != StatusInstalling || created.Source != InstallationSourceManaged {
		t.Fatalf("created = %+v", created)
	}
	if created.LastCheckedAt != nil || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("defaults not applied: %+v", created)
	}

	// Get returns the same row.
	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.ManifestDigest != "sha256:one" {
		t.Fatalf("get = %+v, want %+v", got, created)
	}

	// A second Upsert on the same key replaces the intent columns only.
	again, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey:  key,
		Source:           InstallationSourceImage,
		Status:           StatusInstalled,
		InstalledVersion: "0.151.0",
		ManifestDigest:   "sha256:two",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("upsert must keep the row id: %q vs %q", again.ID, created.ID)
	}
	if again.Source != InstallationSourceImage || again.Status != StatusInstalled || again.InstalledVersion != "0.151.0" || again.ManifestDigest != "sha256:two" {
		t.Fatalf("intent columns not replaced: %+v", again)
	}

	// SetStatus writes status and the failure's code, never the text.
	failed, err := store.SetStatus(ctx, key, StatusFailed, "workspace_dependency.busy")
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
	if failed.Status != StatusFailed || failed.LastErrorCode != "workspace_dependency.busy" || failed.LastError != "" {
		t.Fatalf("set status = %+v", failed)
	}

	// UpdateObserved: nil fields leave columns untouched, pointers write.
	checked := time.Now().UTC().Truncate(time.Microsecond)
	latest := "0.152.0"
	observed, err := store.UpdateObserved(ctx, key, ObservedUpdate{
		LatestVersion: &latest,
		LastCheckedAt: &checked,
	})
	if err != nil {
		t.Fatalf("update observed: %v", err)
	}
	if observed.LatestVersion != latest || observed.LastCheckedAt == nil || !observed.LastCheckedAt.Equal(checked) {
		t.Fatalf("observed columns not written: %+v", observed)
	}
	if observed.Source != InstallationSourceImage || observed.InstalledVersion != "0.151.0" ||
		observed.LastErrorCode != "workspace_dependency.busy" || observed.ManifestDigest != "sha256:two" || observed.Status != StatusFailed {
		t.Fatalf("nil fields must leave columns untouched: %+v", observed)
	}
	empty := ""
	cleared, err := store.UpdateObserved(ctx, key, ObservedUpdate{LastErrorCode: &empty})
	if err != nil {
		t.Fatalf("clear the failure code: %v", err)
	}
	if cleared.LastErrorCode != "" || cleared.LatestVersion != latest || cleared.LastError != "" {
		t.Fatalf("pointer to empty string must clear only the failure code: %+v", cleared)
	}

	// Listing by computer and by status both see the row.
	second := InstallationKey{WorkspaceID: workspaceID, DependencyID: "claude-code"}
	if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: second, Source: InstallationSourceManaged, Status: StatusMissing}); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	forComputer, err := store.ListForWorkspace(ctx, workspaceID)
	if err != nil {
		t.Fatalf("list for workspace: %v", err)
	}
	if len(forComputer) != 2 {
		t.Fatalf("list for workspace = %+v", forComputer)
	}
	missing, err := store.ListByStatus(ctx, StatusMissing)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if !containsKey(missing, second) {
		t.Fatalf("list by status missing = %+v, want %+v", missing, second)
	}

	// A second conditional delete is unmatched; a Get confirms the row is gone.
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(ctx, key); !errors.Is(err, ErrBusy) {
		t.Fatalf("second delete error = %v, want ErrBusy", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("get after delete error = %v, want ErrInstallationNotFound", err)
	}
	if _, err := store.SetStatus(ctx, key, StatusFailed, ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("set status after delete error = %v, want ErrBusy", err)
	}
	if _, err := store.UpdateObserved(ctx, key, ObservedUpdate{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("update observed after delete error = %v, want ErrBusy", err)
	}
}

func TestPostgresStoreStaleOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDependencyPostgres(t, ctx)
	workspaceID := createDependencyComputer(t, ctx, pool)
	store := newIntegrationStore(pool)

	stuck := InstallationKey{WorkspaceID: workspaceID, DependencyID: "codex"}
	fresh := InstallationKey{WorkspaceID: workspaceID, DependencyID: "claude-code"}
	done := InstallationKey{WorkspaceID: workspaceID, DependencyID: "hermes"}
	for _, in := range []UpsertInstallation{
		{InstallationKey: stuck, Source: InstallationSourceManaged, Status: StatusUpdating},
		{InstallationKey: fresh, Source: InstallationSourceManaged, Status: StatusInstalling},
		{InstallationKey: done, Source: InstallationSourceManaged, Status: StatusInstalled},
	} {
		if _, err := store.Upsert(ctx, in); err != nil {
			t.Fatalf("upsert %s: %v", in.DependencyID, err)
		}
	}
	// Age the stuck record and the finished one; only in-progress rows count.
	if _, err := pool.Exec(ctx, `
		UPDATE managed_computer_dependencies
		SET updated_at = now() - interval '1 hour'
		WHERE workspace_id = $1 AND dependency_id IN ('codex', 'hermes')`, workspaceID); err != nil {
		t.Fatalf("age records: %v", err)
	}

	stale, err := store.ListStaleOperations(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	if !containsKey(stale, stuck) {
		t.Fatalf("stale = %+v, want %+v", stale, stuck)
	}
	if containsKey(stale, fresh) || containsKey(stale, done) {
		t.Fatalf("stale must only hold aged in-progress rows: %+v", stale)
	}

	notYet, err := store.ListStaleOperations(ctx, 2*time.Hour)
	if err != nil {
		t.Fatalf("list stale (2h): %v", err)
	}
	if containsKey(notYet, stuck) {
		t.Fatalf("a 1h-old record is not stale at a 2h threshold: %+v", notYet)
	}

	// The reaper's transition: stale -> failed refreshes updated_at.
	reaped, err := store.SetStatus(ctx, stuck, StatusFailed, "operation timed out")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if reaped.Status != StatusFailed || reaped.Status.InProgress() {
		t.Fatalf("reaped = %+v", reaped)
	}
	after, err := store.ListStaleOperations(ctx, 0)
	if err != nil {
		t.Fatalf("list stale (0): %v", err)
	}
	if containsKey(after, stuck) {
		t.Fatalf("failed rows are never stale operations: %+v", after)
	}
}

func TestPostgresStoreRejectsUnknownStatusAndSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDependencyPostgres(t, ctx)
	workspaceID := createDependencyComputer(t, ctx, pool)
	store := newIntegrationStore(pool)
	key := InstallationKey{WorkspaceID: workspaceID, DependencyID: "codex"}

	if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: Status("bogus")}); err == nil {
		t.Fatal("status CHECK must reject unknown values")
	}
	if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: "toolkit", Status: StatusInstalled}); err == nil {
		t.Fatal("source CHECK must reject unknown values")
	}
	if _, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey: InstallationKey{WorkspaceID: workspaceID, DependencyID: ""},
		Source:          InstallationSourceManaged,
		Status:          StatusInstalled,
	}); err == nil {
		t.Fatal("dependency_id CHECK must reject the empty string")
	}
	if _, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey: InstallationKey{WorkspaceID: uuid.NewString(), DependencyID: "codex"},
		Source:          InstallationSourceManaged,
		Status:          StatusInstalled,
	}); err == nil {
		t.Fatal("managed_computers foreign key must reject a workspace without a computer record")
	}
}

func containsKey(items []Installation, key InstallationKey) bool {
	for _, item := range items {
		if item.WorkspaceID == key.WorkspaceID && item.DependencyID == key.DependencyID {
			return true
		}
	}
	return false
}
