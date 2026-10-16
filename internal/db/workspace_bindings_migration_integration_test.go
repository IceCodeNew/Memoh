//go:build integration

package db_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

// Fixture ids for the 0167 boundary. Team one is the default team; team two
// proves the backfill spans teams.
const (
	wbTeamTwo = "00000000-0000-0000-0000-0000000001f2"

	wbUserOne = "10000000-0000-4000-8000-000000000167"
	wbUserTwo = "10000000-0000-4000-8000-000000000168"

	wbBotA = "20000000-0000-4000-8000-00000000016a" // containers + bot_workspaces
	wbBotB = "20000000-0000-4000-8000-00000000016b" // bot_workspaces desired absent, no container
	wbBotC = "20000000-0000-4000-8000-00000000016c" // team two, two remote mounts
	wbBotD = "20000000-0000-4000-8000-00000000016d" // neither slot nor container

	wbRuntimeOne     = "50000000-0000-4000-8000-000000000171" // team two, C's primary
	wbRuntimeTwo     = "50000000-0000-4000-8000-000000000172" // team two
	wbRuntimeRevoked = "50000000-0000-4000-8000-000000000173" // team one, revoked

	wbMountPrimary   = "60000000-0000-4000-8000-000000000171"
	wbMountSecondary = "60000000-0000-4000-8000-000000000172"
	wbWorkdir        = "70000000-0000-4000-8000-000000000171"
)

func seedWorkspaceBindingFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO teams (id, slug) VALUES ($1, 'wb-team-two')`, []any{wbTeamTwo}},
		{`INSERT INTO users (id, username) VALUES ($1, 'wb-owner-one'), ($2, 'wb-owner-two')`, []any{wbUserOne, wbUserTwo}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($3, $4)`, []any{team.DefaultTeamID, wbUserOne, wbTeamTwo, wbUserTwo}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name, status) VALUES
			($1, $5, $6, 'wb-bot-a', 'ready'),
			($2, $5, $6, 'wb-bot-b', 'ready'),
			($4, $5, $6, 'wb-bot-d', 'ready'),
			($3, $7, $8, 'wb-bot-c', 'ready')`,
			[]any{wbBotA, wbBotB, wbBotC, wbBotD, team.DefaultTeamID, wbUserOne, wbTeamTwo, wbUserTwo}},
		{`INSERT INTO containers (team_id, bot_id, container_id, container_name, image, status)
			VALUES ($1, $2, 'ctr-wb-a', 'memoh-wb-a', 'memoh/workspace:test', 'running')`, []any{team.DefaultTeamID, wbBotA}},
		{`INSERT INTO bot_workspaces (bot_id, team_id, desired_state, observed_state) VALUES
			($1, $4, 'present', 'running'),
			($2, $4, 'absent', 'absent'),
			($3, $5, 'present', 'stopped')`, []any{wbBotA, wbBotB, wbBotC, team.DefaultTeamID, wbTeamTwo}},
		{`INSERT INTO user_runtimes (id, team_id, user_id, name, api_token, activated_at, pending_expires_at, revoked_at) VALUES
			($1, $4, $5, 'Office Mac', 'mrk_wb_one', now(), NULL, NULL),
			($2, $4, $5, 'Home PC', 'mrk_wb_two', now(), NULL, NULL),
			($3, $6, $7, 'Old Laptop', 'mrk_wb_old', now(), NULL, now())`,
			[]any{wbRuntimeOne, wbRuntimeTwo, wbRuntimeRevoked, wbTeamTwo, wbUserTwo, team.DefaultTeamID, wbUserOne}},
		{`INSERT INTO bot_remote_runtime_bindings (id, team_id, bot_id, runtime_id, is_primary, tool_approval_config) VALUES
			($1, $3, $4, $5, true, '{"enabled":true,"read":{"mode":"ask"},"write":{"mode":"ask"},"exec":{"mode":"deny"}}'::jsonb),
			($2, $3, $4, $6, false, DEFAULT)`,
			[]any{wbMountPrimary, wbMountSecondary, wbTeamTwo, wbBotC, wbRuntimeOne, wbRuntimeTwo}},
		{`INSERT INTO bot_workdirs (id, team_id, bot_id, name, target_kind, remote_binding_id, path)
			VALUES ($1, $2, $3, 'repo', 'remote', $4, '/Users/two/repo')`, []any{wbWorkdir, wbTeamTwo, wbBotC, wbMountPrimary}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed pre-0167 fixture: %v\n%s", err, stmt.sql)
		}
	}
}

func fkTargetTable(t *testing.T, pool *pgxpool.Pool, table, constraint string) string {
	t.Helper()
	var target string
	if err := pool.QueryRow(context.Background(), `
		SELECT parent.relname
		  FROM pg_constraint con
		  JOIN pg_class parent ON parent.oid = con.confrelid
		 WHERE con.conrelid = ('public.' || quote_ident($1))::regclass
		   AND con.conname = $2`, table, constraint).Scan(&target); err != nil {
		t.Fatalf("read %s.%s: %v", table, constraint, err)
	}
	return target
}

func TestWorkspaceBindingsMigrationBackfillsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	migrateTo(t, dsn, 166)
	seedWorkspaceBindingFixture(t, pool)

	migrateTo(t, dsn, 167)

	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.bot_remote_runtime_bindings') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("bot_remote_runtime_bindings still exists after 0167: exists=%v err=%v", exists, err)
	}

	// Managed workspaces: A (container + slot), B (slot only, desired absent),
	// C (slot). D has neither and gets none. User workspaces: all three
	// runtimes, revoked included.
	type ws struct {
		team, owner, kind, name string
	}
	wantWorkspaces := map[string]ws{
		wbBotA:           {team.DefaultTeamID, wbUserOne, "managed", "wb-bot-a"},
		wbBotB:           {team.DefaultTeamID, wbUserOne, "managed", "wb-bot-b"},
		wbBotC:           {wbTeamTwo, wbUserTwo, "managed", "wb-bot-c"},
		wbRuntimeOne:     {wbTeamTwo, wbUserTwo, "user", "Office Mac"},
		wbRuntimeTwo:     {wbTeamTwo, wbUserTwo, "user", "Home PC"},
		wbRuntimeRevoked: {team.DefaultTeamID, wbUserOne, "user", "Old Laptop"},
	}
	rows, err := pool.Query(ctx, `SELECT id, team_id, owner_user_id, kind, name FROM workspaces`)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	gotWorkspaces := map[string]ws{}
	for rows.Next() {
		var id string
		var got ws
		if err := rows.Scan(&id, &got.team, &got.owner, &got.kind, &got.name); err != nil {
			t.Fatalf("scan workspace: %v", err)
		}
		gotWorkspaces[id] = got
	}
	rows.Close()
	if len(gotWorkspaces) != len(wantWorkspaces) {
		t.Fatalf("workspaces = %v, want %v", gotWorkspaces, wantWorkspaces)
	}
	for id, want := range wantWorkspaces {
		if gotWorkspaces[id] != want {
			t.Errorf("workspace %s = %+v, want %+v", id, gotWorkspaces[id], want)
		}
	}

	// Bindings: the two copied mounts keep their ids and flags; A, B and C
	// each gain a managed binding, primary unless a remote mount already was.
	type binding struct {
		bot, workspace string
		primary        bool
	}
	bindingRows, err := pool.Query(ctx, `SELECT id, bot_id, workspace_id, is_primary FROM bot_workspace_bindings`)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	gotBindings := map[string]binding{}
	for bindingRows.Next() {
		var id string
		var got binding
		if err := bindingRows.Scan(&id, &got.bot, &got.workspace, &got.primary); err != nil {
			t.Fatalf("scan binding: %v", err)
		}
		gotBindings[id] = got
	}
	bindingRows.Close()
	if len(gotBindings) != 5 {
		t.Fatalf("bindings = %d (%v), want 5", len(gotBindings), gotBindings)
	}
	if got := gotBindings[wbMountPrimary]; got != (binding{wbBotC, wbRuntimeOne, true}) {
		t.Errorf("primary mount = %+v, want C -> runtime one, primary", got)
	}
	if got := gotBindings[wbMountSecondary]; got != (binding{wbBotC, wbRuntimeTwo, false}) {
		t.Errorf("secondary mount = %+v, want C -> runtime two", got)
	}
	managedPrimary := map[string]bool{}
	for id, got := range gotBindings {
		if id == wbMountPrimary || id == wbMountSecondary {
			continue
		}
		if got.workspace != got.bot {
			t.Errorf("managed binding %s = %+v, want workspace_id = bot_id", id, got)
		}
		managedPrimary[got.bot] = got.primary
	}
	if !managedPrimary[wbBotA] || !managedPrimary[wbBotB] {
		t.Errorf("A and B must have their managed binding as primary, got %v", managedPrimary)
	}
	if primary, ok := managedPrimary[wbBotC]; !ok || primary {
		t.Errorf("C keeps its remote primary, managed binding must exist and not be primary; got %v", managedPrimary)
	}
	var approvalExec string
	if err := pool.QueryRow(ctx, `SELECT tool_approval_config->'exec'->>'mode' FROM bot_workspace_bindings WHERE id = $1`, wbMountPrimary).Scan(&approvalExec); err != nil || approvalExec != "deny" {
		t.Errorf("copied mount approval exec = %q err=%v, want deny", approvalExec, err)
	}

	// The remote workdir still points at the same binding id and the key
	// now targets the new table.
	var workdirBinding string
	if err := pool.QueryRow(ctx, `SELECT remote_binding_id FROM bot_workdirs WHERE id = $1`, wbWorkdir).Scan(&workdirBinding); err != nil || workdirBinding != wbMountPrimary {
		t.Fatalf("workdir binding = %q err=%v, want %s", workdirBinding, err, wbMountPrimary)
	}
	if target := fkTargetTable(t, pool, "bot_workdirs", "bot_workdirs_remote_binding_fkey"); target != "bot_workspace_bindings" {
		t.Fatalf("bot_workdirs_remote_binding_fkey targets %s, want bot_workspace_bindings", target)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_workdirs (team_id, bot_id, name, target_kind, remote_binding_id, path) VALUES ($1, $2, 'dangling', 'remote', $3, '/nowhere')`,
		wbTeamTwo, wbBotC, "60000000-0000-4000-8000-0000000001ff"); err == nil {
		t.Fatal("a workdir pointing at a missing binding must be rejected")
	}
	for _, table := range []string{"workspaces", "bot_workspace_bindings"} {
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("%s tenant isolation: enabled=%t forced=%t err=%v", table, enabled, forced, err)
		}
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// Re-running the upgrade after the drop is harmless.
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0167_workspaces_and_bindings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("repeat 0167 up: %v", err)
	}
	var bindingCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_workspace_bindings`).Scan(&bindingCount); err != nil || bindingCount != 5 {
		t.Fatalf("bindings after repeat = %d err=%v, want 5", bindingCount, err)
	}

	// Down restores the remote mounts with their ids and flags, drops the
	// managed rows and points bot_workdirs back.
	migrateTo(t, dsn, 166)
	for _, table := range []string{"workspaces", "bot_workspace_bindings"} {
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil || exists {
			t.Fatalf("%s still exists after rollback: exists=%v err=%v", table, exists, err)
		}
	}
	type mount struct {
		bot, runtime, team string
		primary            bool
	}
	mountRows, err := pool.Query(ctx, `SELECT id, bot_id, runtime_id, team_id, is_primary FROM bot_remote_runtime_bindings`)
	if err != nil {
		t.Fatalf("list restored mounts: %v", err)
	}
	gotMounts := map[string]mount{}
	for mountRows.Next() {
		var id string
		var got mount
		if err := mountRows.Scan(&id, &got.bot, &got.runtime, &got.team, &got.primary); err != nil {
			t.Fatalf("scan mount: %v", err)
		}
		gotMounts[id] = got
	}
	mountRows.Close()
	wantMounts := map[string]mount{
		wbMountPrimary:   {wbBotC, wbRuntimeOne, wbTeamTwo, true},
		wbMountSecondary: {wbBotC, wbRuntimeTwo, wbTeamTwo, false},
	}
	if len(gotMounts) != len(wantMounts) {
		t.Fatalf("restored mounts = %v, want %v", gotMounts, wantMounts)
	}
	for id, want := range wantMounts {
		if gotMounts[id] != want {
			t.Errorf("restored mount %s = %+v, want %+v", id, gotMounts[id], want)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT tool_approval_config->'exec'->>'mode' FROM bot_remote_runtime_bindings WHERE id = $1`, wbMountPrimary).Scan(&approvalExec); err != nil || approvalExec != "deny" {
		t.Errorf("restored mount approval exec = %q err=%v, want deny", approvalExec, err)
	}
	if target := fkTargetTable(t, pool, "bot_workdirs", "bot_workdirs_remote_binding_fkey"); target != "bot_remote_runtime_bindings" {
		t.Fatalf("after rollback bot_workdirs_remote_binding_fkey targets %s, want bot_remote_runtime_bindings", target)
	}
	if err := pool.QueryRow(ctx, `SELECT remote_binding_id FROM bot_workdirs WHERE id = $1`, wbWorkdir).Scan(&workdirBinding); err != nil || workdirBinding != wbMountPrimary {
		t.Fatalf("workdir after rollback = %q err=%v, want %s", workdirBinding, err, wbMountPrimary)
	}
	var enabled, forced bool
	if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.bot_remote_runtime_bindings'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("rollback lost tenant isolation: enabled=%t forced=%t err=%v", enabled, forced, err)
	}
	var columns string
	if err := pool.QueryRow(ctx, `
		SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
		  FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='bot_remote_runtime_bindings'`).Scan(&columns); err != nil {
		t.Fatalf("read restored columns: %v", err)
	}
	if columns != "id,bot_id,runtime_id,is_primary,tool_approval_config,created_at,updated_at,team_id" {
		t.Fatalf("restored column order = %q", columns)
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// And up again converges to the same state.
	migrateTo(t, dsn, 167)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_workspace_bindings`).Scan(&bindingCount); err != nil || bindingCount != 5 {
		t.Fatalf("bindings after re-upgrade = %d err=%v, want 5", bindingCount, err)
	}
}

// TestWorkspaceBindingStoreRemoteMountsOverBindings exercises the remote mount
// store against the new tables at the chain head: pairing a runtime creates
// its user workspace, mounting binds it, "primary native" means the managed
// binding, and a bot without a managed binding gets a clear error.
func TestWorkspaceBindingStoreRemoteMountsOverBindings(t *testing.T) {
	ctx := context.Background()
	pool := teamScopedPool(t)
	seedAppBot(t, pool)
	store, err := postgresstore.New(pool)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	// Pairing through the store creates the user workspace in the same
	// transaction when the attach hook asks for it.
	runtime, err := store.CreateUserRuntime(ctx, dbstore.CreateUserRuntimeInput{UserID: appUserID, Name: "Office Mac", APIToken: "mrk_store_test"},
		func(ctx context.Context, q dbstore.Queries, created dbstore.UserRuntimeRecord) error {
			_, err := postgresstore.CreateWorkspaceWith(ctx, q, dbstore.CreateWorkspaceInput{
				ID: created.ID, OwnerUserID: created.UserID, Kind: dbstore.WorkspaceKindUser, Name: created.Name,
			})
			return err
		})
	if err != nil {
		t.Fatalf("CreateUserRuntime(): %v", err)
	}
	workspace, err := store.GetWorkspace(ctx, runtime.ID)
	if err != nil || workspace.Kind != dbstore.WorkspaceKindUser || workspace.OwnerUserID != appUserID {
		t.Fatalf("user workspace = %+v err=%v, want kind user owned by %s", workspace, err, appUserID)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_computers SET activated_at = now(), pending_expires_at = NULL WHERE id = $1`, runtime.ID); err != nil {
		t.Fatalf("activate runtime: %v", err)
	}

	// Without a managed binding, "native" cannot become primary.
	if err := store.SetPrimary(ctx, appBotOneID, "native"); !errors.Is(err, db.ErrNoManagedWorkspaceBinding) {
		t.Fatalf("SetPrimary(native) without managed binding = %v, want ErrNoManagedWorkspaceBinding", err)
	}

	managed, err := store.CreateWorkspace(ctx, dbstore.CreateWorkspaceInput{OwnerUserID: appUserID, Kind: dbstore.WorkspaceKindManaged, Name: "bot-one"})
	if err != nil {
		t.Fatalf("CreateWorkspace(managed): %v", err)
	}
	managedBinding, err := store.UpsertBotWorkspaceBinding(ctx, appBotOneID, managed.ID, true)
	if err != nil || !managedBinding.IsPrimary {
		t.Fatalf("UpsertBotWorkspaceBinding(managed) = %+v, %v", managedBinding, err)
	}

	mount, err := store.CreateOrUpdateMount(ctx, appBotOneID, runtime.ID)
	if err != nil {
		t.Fatalf("CreateOrUpdateMount(): %v", err)
	}
	if mount.RuntimeID != runtime.ID || mount.RuntimeName != "Office Mac" || mount.IsPrimary {
		t.Fatalf("mount = %+v, want runtime %s named Office Mac, not primary", mount, runtime.ID)
	}
	if _, err := store.GetPrimaryMount(ctx, appBotOneID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetPrimaryMount() with managed primary = %v, want ErrNotFound (native)", err)
	}
	if err := store.SetPrimary(ctx, appBotOneID, mount.ID); err != nil {
		t.Fatalf("SetPrimary(mount): %v", err)
	}
	primary, err := store.GetPrimaryMount(ctx, appBotOneID)
	if err != nil || primary.ID != mount.ID {
		t.Fatalf("GetPrimaryMount() = %+v, %v; want the mount", primary, err)
	}
	if b, err := store.GetBotWorkspaceBinding(ctx, appBotOneID, managedBinding.ID); err != nil || b.IsPrimary {
		t.Fatalf("managed binding after remote primary = %+v, %v; want not primary", b, err)
	}
	// The managed binding is not addressable through the remote mount view.
	if _, err := store.GetMount(ctx, appBotOneID, managedBinding.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetMount(managed binding) = %v, want ErrNotFound", err)
	}

	if err := store.SetPrimary(ctx, appBotOneID, "native"); err != nil {
		t.Fatalf("SetPrimary(native): %v", err)
	}
	if b, err := store.GetBotWorkspaceBinding(ctx, appBotOneID, managedBinding.ID); err != nil || !b.IsPrimary {
		t.Fatalf("managed binding after native primary = %+v, %v; want primary", b, err)
	}
	if _, err := store.GetPrimaryMount(ctx, appBotOneID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetPrimaryMount() after native = %v, want ErrNotFound", err)
	}
	grants, err := store.ListGrantsByRuntimeOwner(ctx, appUserID)
	if err != nil || len(grants) != 1 || grants[0].ID != mount.ID || grants[0].IsPrimary {
		t.Fatalf("ListGrantsByRuntimeOwner() = %+v, %v; want the one non-primary mount", grants, err)
	}

	// Deleting a bound workspace is refused; unbinding first releases it.
	if err := store.DeleteWorkspace(ctx, managed.ID); !errors.Is(err, postgresstore.ErrWorkspaceBound) {
		t.Fatalf("DeleteWorkspace(bound) = %v, want ErrWorkspaceBound", err)
	}
	if deleted, err := store.DeleteWorkspaceIfUnbound(ctx, managed.ID); err != nil || deleted {
		t.Fatalf("DeleteWorkspaceIfUnbound(bound) = %v, %v; want false", deleted, err)
	}
	if err := store.DeleteBotWorkspaceBinding(ctx, appBotOneID, managedBinding.ID); err != nil {
		t.Fatalf("DeleteBotWorkspaceBinding(): %v", err)
	}
	if deleted, err := store.DeleteWorkspaceIfUnbound(ctx, managed.ID); err != nil || !deleted {
		t.Fatalf("DeleteWorkspaceIfUnbound(unbound) = %v, %v; want true", deleted, err)
	}
	if err := store.DeleteMount(ctx, appBotOneID, mount.ID); err != nil {
		t.Fatalf("DeleteMount(): %v", err)
	}
	if err := store.DeleteMount(ctx, appBotOneID, mount.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("DeleteMount(twice) = %v, want ErrNotFound", err)
	}

	// Removing the owner's membership cascades to the bot, its bindings and
	// the owner's workspaces in one statement; the deferred binding ->
	// workspace key must not get in the way of that cascade.
	if _, err := store.UpsertBotWorkspaceBinding(ctx, appBotOneID, runtime.ID, true); err != nil {
		t.Fatalf("rebind runtime workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM team_members WHERE user_id = $1`, appUserID); err != nil {
		t.Fatalf("delete membership with bound workspaces: %v", err)
	}
	var bots, workspaces, bindings int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM bots WHERE owner_user_id = $1),
		       (SELECT count(*) FROM workspaces WHERE owner_user_id = $1),
		       (SELECT count(*) FROM bot_workspace_bindings WHERE bot_id = $2)`, appUserID, appBotOneID).Scan(&bots, &workspaces, &bindings); err != nil {
		t.Fatalf("count after membership delete: %v", err)
	}
	if bots != 0 || workspaces != 0 || bindings != 0 {
		t.Fatalf("after membership delete: bots=%d workspaces=%d bindings=%d, want all 0", bots, workspaces, bindings)
	}
}
