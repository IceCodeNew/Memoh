//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

// Fixture ids for the 0169 boundary. Team one is the default team; team two
// proves the backfill spans teams. C's managed workspace id is not the bot id
// (a bot created after 0167), so every mapping must go through the bindings;
// D has no managed computer at all.
const (
	ucTeamTwo = "00000000-0000-0000-0000-0000000001f4"

	ucUserOne = "10000000-0000-4000-8000-000000000169"
	ucUserTwo = "10000000-0000-4000-8000-00000000016a"

	ucBotA = "20000000-0000-4000-8000-00000000019a" // managed (workspace id = bot id) + remote mount
	ucBotC = "20000000-0000-4000-8000-00000000019c" // team two, managed workspace id != bot id
	ucBotD = "20000000-0000-4000-8000-00000000019d" // no managed computer

	ucWorkspaceC = "30000000-0000-4000-8000-00000000019c"

	ucRuntimeOne     = "50000000-0000-4000-8000-000000000191" // team one, A's remote mount
	ucRuntimeOrphan  = "50000000-0000-4000-8000-000000000192" // team two, paired without a workspace row
	ucMountA         = "60000000-0000-4000-8000-000000000191"
	ucWorkdirNativeA = "70000000-0000-4000-8000-000000000191"
	ucWorkdirRemoteA = "70000000-0000-4000-8000-000000000192"
	ucWorkdirNativeC = "70000000-0000-4000-8000-000000000193"
	ucWorkdirNativeD = "70000000-0000-4000-8000-000000000194" // deleted: D has no managed computer
	ucWorkdirDangle  = "70000000-0000-4000-8000-000000000195" // D's remote workdir through A's mount: deleted

	ucSessionA = "80000000-0000-4000-8000-000000000191" // native target snapshot
	ucSessionR = "80000000-0000-4000-8000-000000000192" // remote target snapshot
	ucSessionC = "80000000-0000-4000-8000-000000000193" // native, bot C (workspace id != bot id)
	ucSessionD = "80000000-0000-4000-8000-000000000194" // native, no managed computer: untouched
	ucSessionX = "80000000-0000-4000-8000-000000000195" // no target: untouched

	ucApprovalNative  = "90000000-0000-4000-8000-000000000191"
	ucApprovalRemote  = "90000000-0000-4000-8000-000000000192"
	ucApprovalEmpty   = "90000000-0000-4000-8000-000000000193"
	ucApprovalGone    = "90000000-0000-4000-8000-000000000194" // binding id that no longer exists
	ucApprovalNoMgd   = "90000000-0000-4000-8000-000000000195" // native, bot without managed computer
	ucInputNative     = "a0000000-0000-4000-8000-000000000191"
	ucInputRemote     = "a0000000-0000-4000-8000-000000000192"
	ucGoneBindingID   = "60000000-0000-4000-8000-0000000001ff"
	ucApprovalDenyCfg = `{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"ask","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"deny","bypass_commands":[],"force_review_commands":[]}}`
)

func seedUserComputersFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO teams (id, slug) VALUES ($1, 'uc-team-two')`, []any{ucTeamTwo}},
		{`INSERT INTO users (id, username) VALUES ($1, 'uc-owner-one'), ($2, 'uc-owner-two')`, []any{ucUserOne, ucUserTwo}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($3, $4)`, []any{team.DefaultTeamID, ucUserOne, ucTeamTwo, ucUserTwo}},
		// A carries a non-default native approval policy that must move onto
		// its managed binding.
		{`INSERT INTO bots (id, team_id, owner_user_id, name, status, tool_approval_config) VALUES
			($1, $4, $5, 'uc-bot-a', 'ready', $7::jsonb),
			($3, $4, $5, 'uc-bot-d', 'ready', DEFAULT),
			($2, $6, $8, 'uc-bot-c', 'ready', DEFAULT)`,
			[]any{ucBotA, ucBotC, ucBotD, team.DefaultTeamID, ucUserOne, ucTeamTwo, ucApprovalDenyCfg, ucUserTwo}},
		{`INSERT INTO workspaces (id, team_id, owner_user_id, kind, name) VALUES
			($1, $4, $5, 'managed', 'uc-bot-a'),
			($2, $6, $7, 'managed', 'uc-bot-c'),
			($3, $4, $5, 'user', 'Office Mac')`,
			[]any{ucBotA, ucWorkspaceC, ucRuntimeOne, team.DefaultTeamID, ucUserOne, ucTeamTwo, ucUserTwo}},
		// The orphan runtime was paired without the workspace hook: 0169 gives
		// it a user workspace so the new key holds for every row.
		{`INSERT INTO user_runtimes (id, team_id, user_id, name, api_token, activated_at, pending_expires_at) VALUES
			($1, $3, $4, 'Office Mac', 'mrk_uc_one', now(), NULL),
			($2, $5, $6, 'Spare Laptop', 'mrk_uc_orphan', now(), NULL)`,
			[]any{ucRuntimeOne, ucRuntimeOrphan, team.DefaultTeamID, ucUserOne, ucTeamTwo, ucUserTwo}},
		{`INSERT INTO bot_workspace_bindings (id, team_id, bot_id, workspace_id, is_primary) VALUES
			(gen_random_uuid(), $4, $1, $1, true),
			($3, $4, $1, $5, false),
			(gen_random_uuid(), $6, $2, $7, true)`,
			[]any{ucBotA, ucBotC, ucMountA, team.DefaultTeamID, ucRuntimeOne, ucTeamTwo, ucWorkspaceC}},
		{`INSERT INTO bot_workdirs (id, team_id, bot_id, name, target_kind, remote_binding_id, path) VALUES
			($1, $6, $7, 'site', 'native', NULL, '/data/site'),
			($2, $6, $7, 'repo', 'remote', $10, '/Users/one/repo'),
			($3, $8, $9, 'notes', 'native', NULL, '/data/notes'),
			($4, $6, $11, 'lost', 'native', NULL, '/data/lost'),
			($5, $6, $11, 'dangling', 'remote', $10, '/Users/one/other')`,
			[]any{ucWorkdirNativeA, ucWorkdirRemoteA, ucWorkdirNativeC, ucWorkdirNativeD, ucWorkdirDangle,
				team.DefaultTeamID, ucBotA, ucTeamTwo, ucBotC, ucMountA, ucBotD}},
		{`INSERT INTO bot_sessions (id, team_id, bot_id, type, workdir_id, metadata) VALUES
			($1, $6, $7, 'chat', $10, '{"workspace_target_id":"native","workspace_target":{"target_id":"native","kind":"native","name":"Server Workspace"}}'::jsonb),
			($2, $6, $7, 'chat', NULL, jsonb_build_object('workspace_target_id', $11::text, 'workspace_target', jsonb_build_object('target_id', $11::text, 'kind', 'remote', 'name', 'Office Mac'))),
			($3, $8, $9, 'chat', NULL, '{"workspace_target_id":"native","workspace_target":{"target_id":"native","kind":"native","name":"Server Workspace"}}'::jsonb),
			($4, $6, $12, 'chat', $13, '{"workspace_target_id":"native","workspace_target":{"target_id":"native","kind":"native","name":"Server Workspace"}}'::jsonb),
			($5, $6, $7, 'chat', NULL, '{"title_locked":true}'::jsonb)`,
			[]any{ucSessionA, ucSessionR, ucSessionC, ucSessionD, ucSessionX,
				team.DefaultTeamID, ucBotA, ucTeamTwo, ucBotC, ucWorkdirNativeA, ucMountA, ucBotD, ucWorkdirNativeD}},
		{`INSERT INTO tool_approval_requests (id, team_id, bot_id, session_id, workspace_target_id, tool_call_id, tool_name, operation, tool_input, short_id) VALUES
			($1, $6, $7, $8, 'native', 'call-1', 'write', 'write', '{}'::jsonb, 1),
			($2, $6, $7, $8, $9, 'call-2', 'exec', 'exec', '{}'::jsonb, 2),
			($3, $6, $7, $8, '', 'call-3', 'read', 'read', '{}'::jsonb, 3),
			($4, $6, $7, $8, $10, 'call-4', 'read', 'read', '{}'::jsonb, 4),
			($5, $6, $11, $12, 'native', 'call-5', 'read', 'read', '{}'::jsonb, 1)`,
			[]any{ucApprovalNative, ucApprovalRemote, ucApprovalEmpty, ucApprovalGone, ucApprovalNoMgd,
				team.DefaultTeamID, ucBotA, ucSessionA, ucMountA, ucGoneBindingID, ucBotD, ucSessionD}},
		{`INSERT INTO user_input_requests (id, team_id, bot_id, session_id, workspace_target_id, tool_call_id, short_id, input_json) VALUES
			($1, $3, $4, $5, 'native', 'ask-1', 1, '{}'::jsonb),
			($2, $3, $4, $5, $6, 'ask-2', 2, '{}'::jsonb)`,
			[]any{ucInputNative, ucInputRemote, team.DefaultTeamID, ucBotA, ucSessionA, ucMountA}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed pre-0169 fixture: %v\n%s", err, stmt.sql)
		}
	}
}

func scanNullableText(t *testing.T, pool *pgxpool.Pool, query string, args ...any) (string, bool) {
	t.Helper()
	var value *string
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if value == nil {
		return "", false
	}
	return *value, true
}

func constraintNames(t *testing.T, pool *pgxpool.Pool, table string) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT conname FROM pg_constraint
		 WHERE conrelid = ('public.' || quote_ident($1))::regclass`, table)
	if err != nil {
		t.Fatalf("list constraints of %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

func columnOrder(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var columns string
	if err := pool.QueryRow(context.Background(), `
		SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
		  FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1`, table).Scan(&columns); err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	return columns
}

func TestUserComputersMigrationBackfillsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	// The fresh install is the reference for everything the upgrade must
	// converge to: constraint names, index names and column order.
	wantUserComputersConstraints := constraintNames(t, pool, "user_computers")
	wantWorkdirColumns := columnOrder(t, pool, "bot_workdirs")
	wantApprovalColumns := columnOrder(t, pool, "tool_approval_requests")
	wantInputColumns := columnOrder(t, pool, "user_input_requests")
	// The legacy chain re-creates user_runtimes (0107) and the text target
	// columns (0110) on the way up; 0169 must leave none of them behind.
	if tableExists(t, pool, "user_runtimes") {
		t.Fatal("fresh install keeps a user_runtimes shell")
	}
	for _, table := range []string{"tool_approval_requests", "user_input_requests"} {
		if n := countRows(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'workspace_target_id'`, table); n != 0 {
			t.Fatalf("fresh install keeps %s.workspace_target_id", table)
		}
	}

	migrateTo(t, dsn, 168)
	if !tableExists(t, pool, "user_runtimes") || tableExists(t, pool, "user_computers") {
		t.Fatal("rollback to 0168 did not restore user_runtimes")
	}
	seedUserComputersFixture(t, pool)
	preWorkdirColumns := columnOrder(t, pool, "bot_workdirs")
	preApprovalColumns := columnOrder(t, pool, "tool_approval_requests")
	preInputColumns := columnOrder(t, pool, "user_input_requests")

	migrateTo(t, dsn, 169)

	// user_runtimes -> user_computers: rows, names and the workspace key.
	if tableExists(t, pool, "user_runtimes") || !tableExists(t, pool, "user_computers") {
		t.Fatal("user_runtimes was not renamed to user_computers")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM user_computers`); n != 2 {
		t.Fatalf("user_computers rows = %d, want 2", n)
	}
	gotConstraints := constraintNames(t, pool, "user_computers")
	for name := range wantUserComputersConstraints {
		if !gotConstraints[name] {
			t.Errorf("user_computers lacks constraint %s that a fresh install has", name)
		}
	}
	for name := range gotConstraints {
		if !wantUserComputersConstraints[name] {
			t.Errorf("user_computers carries constraint %s that a fresh install lacks", name)
		}
	}
	if target := fkTargetTable(t, pool, "user_computers", "user_computers_workspace_fkey"); target != "workspaces" {
		t.Fatalf("user_computers_workspace_fkey targets %s, want workspaces", target)
	}
	for _, index := range []string{"idx_user_computers_active_user_name", "idx_user_computers_user_id"} {
		if n := countRows(t, pool, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`, index); n != 1 {
			t.Errorf("index %s missing after rename", index)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'user_computers' AND policyname LIKE 'user_computers_team_%'`); n != 4 {
		t.Errorf("user_computers policies = %d, want 4 under the new name", n)
	}
	// The orphan runtime got its user workspace; a new computer without one
	// is refused by the key.
	if n := countRows(t, pool, `SELECT count(*) FROM workspaces WHERE id = $1 AND kind = 'user' AND team_id = $2`, ucRuntimeOrphan, ucTeamTwo); n != 1 {
		t.Fatalf("orphan runtime workspace rows = %d, want 1", n)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_computers (id, team_id, user_id, name, api_token) VALUES ($1, $2, $3, 'no workspace', 'mrk_uc_none')`,
		"50000000-0000-4000-8000-0000000001fe", team.DefaultTeamID, ucUserOne); err == nil {
		t.Fatal("a computer without a workspace row must be rejected")
	}

	// bot_workdirs: workspace_id in target_kind's slot, same order as a fresh
	// install; native rows of a bot without a managed computer and remote
	// rows bound through another bot are gone.
	if got := columnOrder(t, pool, "bot_workdirs"); got != wantWorkdirColumns {
		t.Fatalf("bot_workdirs columns after upgrade = %q, want fresh-install order %q", got, wantWorkdirColumns)
	}
	wantWorkdirs := map[string]string{
		ucWorkdirNativeA: ucBotA,
		ucWorkdirRemoteA: ucRuntimeOne,
		ucWorkdirNativeC: ucWorkspaceC,
	}
	if n := countRows(t, pool, `SELECT count(*) FROM bot_workdirs`); n != len(wantWorkdirs) {
		t.Fatalf("bot_workdirs rows = %d, want %d", n, len(wantWorkdirs))
	}
	for id, want := range wantWorkdirs {
		got, ok := scanNullableText(t, pool, `SELECT workspace_id::text FROM bot_workdirs WHERE id = $1`, id)
		if !ok || got != want {
			t.Errorf("workdir %s workspace = %q (ok=%v), want %s", id, got, ok, want)
		}
	}
	if target := fkTargetTable(t, pool, "bot_workdirs", "bot_workdirs_binding_fkey"); target != "bot_workspace_bindings" {
		t.Fatalf("bot_workdirs_binding_fkey targets %s", target)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_workdirs (team_id, bot_id, workspace_id, name, path) VALUES ($1, $2, $3, 'unbound', '/data/x')`,
		team.DefaultTeamID, ucBotA, ucWorkspaceC); err == nil {
		t.Fatal("a workdir on a workspace the bot is not bound to must be rejected")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_workdirs (team_id, bot_id, workspace_id, name, path) VALUES ($1, $2, $2, 'dup', '/data/site')`,
		team.DefaultTeamID, ucBotA); err == nil {
		t.Fatal("a second live workdir for the same directory on the same workspace must be rejected")
	}
	// The session pinned to D's deleted workdir lost its pin; A's kept it.
	if _, ok := scanNullableText(t, pool, `SELECT workdir_id::text FROM bot_sessions WHERE id = $1`, ucSessionD); ok {
		t.Error("session bound to a deleted workdir still points at it")
	}
	if got, ok := scanNullableText(t, pool, `SELECT workdir_id::text FROM bot_sessions WHERE id = $1`, ucSessionA); !ok || got != ucWorkdirNativeA {
		t.Errorf("session A workdir = %q (ok=%v), want %s", got, ok, ucWorkdirNativeA)
	}

	// Approval / input requests: text target -> workspace id in place.
	if got := columnOrder(t, pool, "tool_approval_requests"); got != wantApprovalColumns {
		t.Fatalf("tool_approval_requests columns = %q, want %q", got, wantApprovalColumns)
	}
	if got := columnOrder(t, pool, "user_input_requests"); got != wantInputColumns {
		t.Fatalf("user_input_requests columns = %q, want %q", got, wantInputColumns)
	}
	for id, want := range map[string]string{
		ucApprovalNative: ucBotA,
		ucApprovalRemote: ucRuntimeOne,
		ucApprovalEmpty:  ucBotA,
	} {
		got, ok := scanNullableText(t, pool, `SELECT workspace_id::text FROM tool_approval_requests WHERE id = $1`, id)
		if !ok || got != want {
			t.Errorf("approval %s workspace = %q (ok=%v), want %s", id, got, ok, want)
		}
	}
	for _, id := range []string{ucApprovalGone, ucApprovalNoMgd} {
		if got, ok := scanNullableText(t, pool, `SELECT workspace_id::text FROM tool_approval_requests WHERE id = $1`, id); ok {
			t.Errorf("approval %s workspace = %q, want NULL (target no longer resolves)", id, got)
		}
	}
	if got, ok := scanNullableText(t, pool, `SELECT workspace_id::text FROM user_input_requests WHERE id = $1`, ucInputNative); !ok || got != ucBotA {
		t.Errorf("input native workspace = %q (ok=%v), want %s", got, ok, ucBotA)
	}
	if got, ok := scanNullableText(t, pool, `SELECT workspace_id::text FROM user_input_requests WHERE id = $1`, ucInputRemote); !ok || got != ucRuntimeOne {
		t.Errorf("input remote workspace = %q (ok=%v), want %s", got, ok, ucRuntimeOne)
	}

	// Session snapshots gain workspace_id and keep their client-facing ids.
	for id, want := range map[string]string{ucSessionA: ucBotA, ucSessionR: ucRuntimeOne, ucSessionC: ucWorkspaceC} {
		got, ok := scanNullableText(t, pool, `SELECT metadata->'workspace_target'->>'workspace_id' FROM bot_sessions WHERE id = $1`, id)
		if !ok || got != want {
			t.Errorf("session %s snapshot workspace = %q (ok=%v), want %s", id, got, ok, want)
		}
	}
	if got, _ := scanNullableText(t, pool, `SELECT metadata->>'workspace_target_id' FROM bot_sessions WHERE id = $1`, ucSessionR); got != ucMountA {
		t.Errorf("session R workspace_target_id = %q, want the mount id %s kept", got, ucMountA)
	}
	if got, _ := scanNullableText(t, pool, `SELECT metadata->'workspace_target'->>'target_id' FROM bot_sessions WHERE id = $1`, ucSessionA); got != "native" {
		t.Errorf("session A target_id = %q, want native kept", got)
	}
	if _, ok := scanNullableText(t, pool, `SELECT metadata->'workspace_target'->>'workspace_id' FROM bot_sessions WHERE id = $1`, ucSessionD); ok {
		t.Error("session of a bot without managed computer gained a workspace_id")
	}
	if got, _ := scanNullableText(t, pool, `SELECT metadata::text FROM bot_sessions WHERE id = $1`, ucSessionX); got != `{"title_locked": true}` {
		t.Errorf("session without target changed: %s", got)
	}

	// The native approval policy moved onto the managed binding; the bot
	// column stays.
	if got, _ := scanNullableText(t, pool, `SELECT tool_approval_config->'exec'->>'mode' FROM bot_workspace_bindings WHERE bot_id = $1 AND workspace_id = $1`, ucBotA); got != "deny" {
		t.Errorf("A managed binding exec mode = %q, want deny copied from bots", got)
	}
	if got, _ := scanNullableText(t, pool, `SELECT tool_approval_config->'exec'->>'mode' FROM bots WHERE id = $1`, ucBotA); got != "deny" {
		t.Errorf("bots.tool_approval_config lost: exec mode = %q", got)
	}
	if got, _ := scanNullableText(t, pool, `SELECT tool_approval_config->'exec'->>'mode' FROM bot_workspace_bindings WHERE id = $1`, ucMountA); got != "allow" {
		t.Errorf("remote mount approval changed: exec mode = %q", got)
	}

	for _, table := range []string{"user_computers", "bot_workdirs", "tool_approval_requests", "user_input_requests", "bot_sessions"} {
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("%s tenant isolation: enabled=%t forced=%t err=%v", table, enabled, forced, err)
		}
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// Re-running the upgrade is harmless.
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0169_user_computers_and_workspace_targets.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("repeat 0169 up: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM bot_workdirs`); n != len(wantWorkdirs) {
		t.Fatalf("bot_workdirs after repeat = %d, want %d", n, len(wantWorkdirs))
	}

	// Down: the text targets, the two workdir columns in their original
	// positions, user_runtimes under its old names, the snapshot key gone.
	migrateTo(t, dsn, 168)
	if tableExists(t, pool, "user_computers") || !tableExists(t, pool, "user_runtimes") {
		t.Fatal("rollback did not restore user_runtimes")
	}
	restored := constraintNames(t, pool, "user_runtimes")
	for _, name := range []string{"user_runtimes_pkey", "user_runtimes_api_token_key", "user_runtimes_activation_state_check", "user_runtimes_user_id_fkey", "memoh_team_key_eeb71d700aa6"} {
		if !restored[name] {
			t.Errorf("rollback lost user_runtimes constraint %s", name)
		}
	}
	if restored["user_computers_workspace_fkey"] {
		t.Error("rollback kept the workspace key on user_runtimes")
	}
	if got := columnOrder(t, pool, "bot_workdirs"); got != preWorkdirColumns {
		t.Fatalf("bot_workdirs columns after rollback = %q, want %q", got, preWorkdirColumns)
	}
	if got := columnOrder(t, pool, "tool_approval_requests"); got != preApprovalColumns {
		t.Fatalf("tool_approval_requests columns after rollback = %q, want %q", got, preApprovalColumns)
	}
	if got := columnOrder(t, pool, "user_input_requests"); got != preInputColumns {
		t.Fatalf("user_input_requests columns after rollback = %q, want %q", got, preInputColumns)
	}
	type workdir struct {
		kind, binding string
	}
	wantRestored := map[string]workdir{
		ucWorkdirNativeA: {"native", ""},
		ucWorkdirRemoteA: {"remote", ucMountA},
		ucWorkdirNativeC: {"native", ""},
	}
	for id, want := range wantRestored {
		var kind string
		var binding *string
		if err := pool.QueryRow(ctx, `SELECT target_kind, remote_binding_id::text FROM bot_workdirs WHERE id = $1`, id).Scan(&kind, &binding); err != nil {
			t.Fatalf("read restored workdir %s: %v", id, err)
		}
		got := workdir{kind: kind}
		if binding != nil {
			got.binding = *binding
		}
		if got != want {
			t.Errorf("restored workdir %s = %+v, want %+v", id, got, want)
		}
	}
	if target := fkTargetTable(t, pool, "bot_workdirs", "bot_workdirs_remote_binding_fkey"); target != "bot_workspace_bindings" {
		t.Fatalf("after rollback bot_workdirs_remote_binding_fkey targets %s", target)
	}
	if target := fkTargetTable(t, pool, "bot_sessions", "bot_sessions_workdir_id_fkey"); target != "bot_workdirs" {
		t.Fatalf("after rollback bot_sessions_workdir_id_fkey targets %s", target)
	}
	if got, ok := scanNullableText(t, pool, `SELECT workdir_id::text FROM bot_sessions WHERE id = $1`, ucSessionA); !ok || got != ucWorkdirNativeA {
		t.Errorf("session A workdir after rollback = %q (ok=%v), want %s", got, ok, ucWorkdirNativeA)
	}
	for id, want := range map[string]string{
		ucApprovalNative: "native",
		ucApprovalRemote: ucMountA,
		ucApprovalEmpty:  "native",
		ucApprovalGone:   "",
		ucApprovalNoMgd:  "",
	} {
		if got, _ := scanNullableText(t, pool, `SELECT workspace_target_id FROM tool_approval_requests WHERE id = $1`, id); got != want {
			t.Errorf("restored approval %s target = %q, want %q", id, got, want)
		}
	}
	if got, _ := scanNullableText(t, pool, `SELECT workspace_target_id FROM user_input_requests WHERE id = $1`, ucInputRemote); got != ucMountA {
		t.Errorf("restored input target = %q, want %s", got, ucMountA)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM bot_sessions WHERE metadata->'workspace_target' ? 'workspace_id'`); n != 0 {
		t.Errorf("%d session snapshots still carry workspace_id after rollback", n)
	}
	if got, _ := scanNullableText(t, pool, `SELECT metadata->>'workspace_target_id' FROM bot_sessions WHERE id = $1`, ucSessionR); got != ucMountA {
		t.Errorf("session R target after rollback = %q", got)
	}
	var enabled, forced bool
	if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.bot_workdirs'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("rollback lost bot_workdirs tenant isolation: enabled=%t forced=%t err=%v", enabled, forced, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'user_runtimes' AND policyname LIKE 'user_runtimes_team_%'`); n != 4 {
		t.Errorf("user_runtimes policies after rollback = %d, want 4", n)
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// And up again converges to the same state.
	migrateTo(t, dsn, 169)
	if got := columnOrder(t, pool, "bot_workdirs"); got != wantWorkdirColumns {
		t.Fatalf("bot_workdirs columns after re-upgrade = %q", got)
	}
	if got, ok := scanNullableText(t, pool, `SELECT workspace_id::text FROM bot_workdirs WHERE id = $1`, ucWorkdirRemoteA); !ok || got != ucRuntimeOne {
		t.Errorf("remote workdir after re-upgrade = %q (ok=%v)", got, ok)
	}
}
