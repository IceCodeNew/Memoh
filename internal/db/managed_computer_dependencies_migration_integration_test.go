//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

// Fixture ids for the 0170 boundary. Team one is the default team; team two
// proves the backfill spans teams. A keeps the bot id as its workspace id
// (0167 backfill); C was created after 0167 and has a workspace id of its
// own; D has no managed computer; E has a managed binding but its computer
// was never provisioned (no managed_computers row); F is bound to A's
// computer after A, so its rows collide with A's.
const (
	mcdTeamTwo = "00000000-0000-0000-0000-0000000001f5"

	mcdUserOne = "10000000-0000-4000-8000-000000000170"
	mcdUserTwo = "10000000-0000-4000-8000-000000000171"

	mcdBotA = "20000000-0000-4000-8000-0000000001a0" // managed, workspace id = bot id
	mcdBotC = "20000000-0000-4000-8000-0000000001a1" // team two, workspace id != bot id
	mcdBotD = "20000000-0000-4000-8000-0000000001a2" // no managed binding: rows deleted
	mcdBotE = "20000000-0000-4000-8000-0000000001a3" // binding, no managed_computers row: rows deleted
	mcdBotF = "20000000-0000-4000-8000-0000000001a4" // shares A's computer, bound later

	mcdWorkspaceC = "30000000-0000-4000-8000-0000000001a1"
	mcdWorkspaceE = "30000000-0000-4000-8000-0000000001a3"

	mcdRevision    = "4111dba29084f12372fb90545b0e85f8fd5c6a87e74e9203068526bc69095ac2"
	mcdOperationID = "0123456789abcdef0123456789abcdef"
)

func seedManagedComputerDependenciesFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO teams (id, slug) VALUES ($1, 'mcd-team-two')`, []any{mcdTeamTwo}},
		{`INSERT INTO users (id, username) VALUES ($1, 'mcd-owner-one'), ($2, 'mcd-owner-two')`, []any{mcdUserOne, mcdUserTwo}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($3, $4)`, []any{team.DefaultTeamID, mcdUserOne, mcdTeamTwo, mcdUserTwo}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name, status) VALUES
			($1, $6, $7, 'mcd-bot-a', 'ready'),
			($3, $6, $7, 'mcd-bot-d', 'ready'),
			($4, $6, $7, 'mcd-bot-e', 'ready'),
			($5, $6, $7, 'mcd-bot-f', 'ready'),
			($2, $8, $9, 'mcd-bot-c', 'ready')`,
			[]any{mcdBotA, mcdBotC, mcdBotD, mcdBotE, mcdBotF, team.DefaultTeamID, mcdUserOne, mcdTeamTwo, mcdUserTwo}},
		{`INSERT INTO workspaces (id, team_id, owner_user_id, kind, name) VALUES
			($1, $4, $5, 'managed', 'mcd-bot-a'),
			($3, $4, $5, 'managed', 'mcd-bot-e'),
			($2, $6, $7, 'managed', 'mcd-bot-c')`,
			[]any{mcdBotA, mcdWorkspaceC, mcdWorkspaceE, team.DefaultTeamID, mcdUserOne, mcdTeamTwo, mcdUserTwo}},
		// A was bound to its computer before F; the binding order decides
		// whose record wins a collision.
		{`INSERT INTO bot_workspace_bindings (team_id, bot_id, workspace_id, is_primary, created_at) VALUES
			($1, $2, $2, true, now() - interval '2 hours'),
			($1, $3, $2, true, now() - interval '1 hour'),
			($1, $4, $5, true, now()),
			($6, $7, $8, true, now())`,
			[]any{team.DefaultTeamID, mcdBotA, mcdBotF, mcdBotE, mcdWorkspaceE, mcdTeamTwo, mcdBotC, mcdWorkspaceC}},
		{`INSERT INTO managed_computers (workspace_id, team_id, desired_state) VALUES
			($1, $3, 'present'),
			($2, $4, 'present')`,
			[]any{mcdBotA, mcdWorkspaceC, team.DefaultTeamID, mcdTeamTwo}},
		// Both sources, an in-flight claim, a recorded failure code and the
		// publication columns, so the rename is shown to keep every column.
		{`INSERT INTO bot_dependency_installations
			(team_id, bot_id, dependency_id, source, status, installed_version, latest_version, last_error_code, manifest_digest, source_url, registry_id, definition_revision, operation_id) VALUES
			($1, $2, 'codex',   'managed', 'installed',  '0.50.0', '0.51.0', '',                          'sha256:a', 'https://supermarket.example', 'memoh', $6, ''),
			($1, $2, 'python',  'image',   'installed',  '3.12.0', '',       '',                          '',         '',                            '',      '', ''),
			($1, $2, 'node',    'managed', 'installing', '',       '',       '',                          'sha256:n', 'https://supermarket.example', 'memoh', $6, $7),
			($1, $2, 'uv',      'managed', 'failed',     '',       '',       'workspace_dependency.busy', '',         '',                            '',      '', ''),
			($1, $3, 'codex',   'managed', 'installed',  '0.40.0', '',       '',                          'sha256:f', 'https://supermarket.example', 'memoh', $6, ''),
			($1, $3, 'ripgrep', 'managed', 'installed',  '14.0.0', '',       '',                          'sha256:r', 'https://supermarket.example', 'memoh', $6, ''),
			($1, $4, 'codex',   'managed', 'installed',  '0.30.0', '',       '',                          '',         '',                            '',      '', ''),
			($1, $5, 'codex',   'managed', 'installed',  '0.20.0', '',       '',                          '',         '',                            '',      '', ''),
			($8, $9, 'codex',   'managed', 'installed',  '0.50.0', '',       '',                          'sha256:c', 'https://supermarket.example', 'memoh', $6, '')`,
			[]any{team.DefaultTeamID, mcdBotA, mcdBotF, mcdBotD, mcdBotE, mcdRevision, mcdOperationID, mcdTeamTwo, mcdBotC}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed pre-0170 fixture: %v\n%s", err, stmt.sql)
		}
	}
}

// dependencyKeys lists "<key>/<dependency_id>" of every record, keyColumn
// being workspace_id or bot_id depending on the shape under test.
func dependencyKeys(t *testing.T, pool *pgxpool.Pool, table, keyColumn string) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT `+keyColumn+`::text || '/' || dependency_id FROM public.`+table)
	if err != nil {
		t.Fatalf("list %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		out[key] = true
	}
	return out
}

func assertSameKeys(t *testing.T, what string, got, want map[string]bool) {
	t.Helper()
	for key := range want {
		if !got[key] {
			t.Errorf("%s lacks %s", what, key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("%s carries unexpected %s", what, key)
		}
	}
}

func TestManagedComputerDependenciesMigrationBackfillsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	// The fresh install is the reference the upgrade must converge to.
	wantConstraints := constraintNames(t, pool, "managed_computer_dependencies")
	wantColumns := columnOrder(t, pool, "managed_computer_dependencies")
	if !strings.HasSuffix(wantColumns, ",workspace_id") {
		t.Fatalf("fresh install must declare workspace_id last (the upgrade appends it): %q", wantColumns)
	}
	// The legacy 0148 re-creates bot_dependency_installations on the way up;
	// 0170 must leave no shell behind.
	if tableExists(t, pool, "bot_dependency_installations") {
		t.Fatal("fresh install keeps a bot_dependency_installations shell")
	}
	if target := fkTargetTable(t, pool, "managed_computer_dependencies", "managed_computer_dependencies_workspace_id_fkey"); target != "managed_computers" {
		t.Fatalf("fresh managed_computer_dependencies_workspace_id_fkey targets %s", target)
	}

	migrateTo(t, dsn, 169)
	if !tableExists(t, pool, "bot_dependency_installations") || tableExists(t, pool, "managed_computer_dependencies") {
		t.Fatal("rollback to 0169 did not restore bot_dependency_installations")
	}
	preColumns := columnOrder(t, pool, "bot_dependency_installations")
	seedManagedComputerDependenciesFixture(t, pool)

	migrateTo(t, dsn, 170)

	if tableExists(t, pool, "bot_dependency_installations") || !tableExists(t, pool, "managed_computer_dependencies") {
		t.Fatal("bot_dependency_installations was not renamed to managed_computer_dependencies")
	}
	if got := columnOrder(t, pool, "managed_computer_dependencies"); got != wantColumns {
		t.Fatalf("columns after upgrade = %q, want fresh-install order %q", got, wantColumns)
	}
	gotConstraints := constraintNames(t, pool, "managed_computer_dependencies")
	for name := range wantConstraints {
		if !gotConstraints[name] {
			t.Errorf("managed_computer_dependencies lacks constraint %s that a fresh install has", name)
		}
	}
	for name := range gotConstraints {
		if !wantConstraints[name] {
			t.Errorf("managed_computer_dependencies carries constraint %s that a fresh install lacks", name)
		}
	}
	if target := fkTargetTable(t, pool, "managed_computer_dependencies", "managed_computer_dependencies_workspace_id_fkey"); target != "managed_computers" {
		t.Fatalf("managed_computer_dependencies_workspace_id_fkey targets %s, want managed_computers", target)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'managed_computer_dependencies' AND policyname LIKE 'managed_computer_dependencies_team_%'`); n != 4 {
		t.Errorf("managed_computer_dependencies policies = %d, want 4 under the new name", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'managed_computer_dependencies' AND policyname LIKE 'bot_dependency_installations_%'`); n != 0 {
		t.Errorf("%d policies still carry the old name", n)
	}
	var enabled, forced bool
	if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.managed_computer_dependencies'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("managed_computer_dependencies tenant isolation: enabled=%t forced=%t err=%v", enabled, forced, err)
	}

	// Rows: A's four and F's ripgrep on A's computer, C's on its own
	// computer; D (no computer), E (no computer record) and F's colliding
	// codex are gone.
	wantKeys := map[string]bool{
		mcdBotA + "/codex":       true,
		mcdBotA + "/python":      true,
		mcdBotA + "/node":        true,
		mcdBotA + "/uv":          true,
		mcdBotA + "/ripgrep":     true,
		mcdWorkspaceC + "/codex": true,
	}
	assertSameKeys(t, "managed_computer_dependencies after upgrade", dependencyKeys(t, pool, "managed_computer_dependencies", "workspace_id"), wantKeys)
	// The surviving codex on A's computer is A's row, not F's.
	if got, _ := scanNullableText(t, pool, `SELECT installed_version FROM managed_computer_dependencies WHERE workspace_id = $1 AND dependency_id = 'codex'`, mcdBotA); got != "0.50.0" {
		t.Errorf("codex on A's computer = %q, want A's 0.50.0 (first bound bot wins)", got)
	}
	// Every other column rode along.
	var source, status, lastErrorCode, digest, revision, operationID, teamID string
	if err := pool.QueryRow(ctx, `SELECT source, status, last_error_code, manifest_digest, definition_revision, operation_id, team_id::text FROM managed_computer_dependencies WHERE workspace_id = $1 AND dependency_id = 'node'`, mcdBotA).
		Scan(&source, &status, &lastErrorCode, &digest, &revision, &operationID, &teamID); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if source != "managed" || status != "installing" || digest != "sha256:n" || revision != mcdRevision || operationID != mcdOperationID || teamID != team.DefaultTeamID {
		t.Errorf("node row changed: %s %s %s %s %s %s", source, status, digest, revision, operationID, teamID)
	}
	if got, _ := scanNullableText(t, pool, `SELECT last_error_code FROM managed_computer_dependencies WHERE workspace_id = $1 AND dependency_id = 'uv'`, mcdBotA); got != "workspace_dependency.busy" {
		t.Errorf("uv last_error_code = %q, want kept", got)
	}
	if got, _ := scanNullableText(t, pool, `SELECT source FROM managed_computer_dependencies WHERE workspace_id = $1 AND dependency_id = 'python'`, mcdBotA); got != "image" {
		t.Errorf("python source = %q, want image kept", got)
	}
	if got, _ := scanNullableText(t, pool, `SELECT team_id::text FROM managed_computer_dependencies WHERE workspace_id = $1`, mcdWorkspaceC); got != mcdTeamTwo {
		t.Errorf("C's record team = %q, want team two", got)
	}

	// The new key: one record per (computer, dependency), on a computer
	// that has a record.
	if _, err := pool.Exec(ctx, `INSERT INTO managed_computer_dependencies (team_id, workspace_id, dependency_id, source, status) VALUES ($1, $2, 'codex', 'managed', 'installed')`,
		team.DefaultTeamID, mcdBotA); sqlState(err) != "23505" {
		t.Fatalf("a second record for the same dependency on the same computer must be rejected (23505), got %q (%v)", sqlState(err), err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO managed_computer_dependencies (team_id, workspace_id, dependency_id, source, status) VALUES ($1, $2, 'codex', 'managed', 'installed')`,
		team.DefaultTeamID, mcdWorkspaceE); sqlState(err) != "23503" {
		t.Fatalf("a record on a workspace without a managed computer must be rejected (23503), got %q (%v)", sqlState(err), err)
	}
	// Records go with their computer.
	if _, err := pool.Exec(ctx, `INSERT INTO managed_computers (workspace_id, team_id, desired_state) VALUES ($1, $2, 'present')`, mcdWorkspaceE, team.DefaultTeamID); err != nil {
		t.Fatalf("provision E: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO managed_computer_dependencies (team_id, workspace_id, dependency_id, source, status) VALUES ($1, $2, 'codex', 'managed', 'installed')`,
		team.DefaultTeamID, mcdWorkspaceE); err != nil {
		t.Fatalf("record on E: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM managed_computers WHERE workspace_id = $1`, mcdWorkspaceE); err != nil {
		t.Fatalf("remove E's computer: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computer_dependencies WHERE workspace_id = $1`, mcdWorkspaceE); n != 0 {
		t.Errorf("records of a removed computer = %d, want cascade to 0", n)
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// Re-running the upgrade is harmless.
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0170_managed_computer_dependencies.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("repeat 0170 up: %v", err)
	}
	assertSameKeys(t, "managed_computer_dependencies after repeat", dependencyKeys(t, pool, "managed_computer_dependencies", "workspace_id"), wantKeys)
	if got := columnOrder(t, pool, "managed_computer_dependencies"); got != wantColumns {
		t.Fatalf("columns after repeat = %q, want %q", got, wantColumns)
	}

	// Down: bot_id back from the first bound bot (F's ripgrep now belongs
	// to A), old names, old key.
	migrateTo(t, dsn, 169)
	if tableExists(t, pool, "managed_computer_dependencies") || !tableExists(t, pool, "bot_dependency_installations") {
		t.Fatal("rollback did not restore bot_dependency_installations")
	}
	if got := columnOrder(t, pool, "bot_dependency_installations"); got != preColumns {
		t.Fatalf("columns after rollback = %q, want %q", got, preColumns)
	}
	restored := constraintNames(t, pool, "bot_dependency_installations")
	for _, name := range []string{"bot_dependency_installations_pkey", "bot_dependency_installations_identity_key", "bot_dependency_installations_bot_id_fkey", "bot_dependency_installations_team_id_fkey", "bot_dependency_installations_status_check", "bot_dependency_installations_operation_id_check", "memoh_team_key_07a3be5666c2"} {
		if !restored[name] {
			t.Errorf("rollback lost constraint %s", name)
		}
	}
	for name := range restored {
		if strings.HasPrefix(name, "managed_computer_dependencies") || name == "memoh_team_key_5ab3fe40cf41" {
			t.Errorf("rollback kept new-name constraint %s", name)
		}
	}
	if target := fkTargetTable(t, pool, "bot_dependency_installations", "bot_dependency_installations_bot_id_fkey"); target != "bots" {
		t.Fatalf("after rollback bot_dependency_installations_bot_id_fkey targets %s", target)
	}
	assertSameKeys(t, "bot_dependency_installations after rollback", dependencyKeys(t, pool, "bot_dependency_installations", "bot_id"), map[string]bool{
		mcdBotA + "/codex":   true,
		mcdBotA + "/python":  true,
		mcdBotA + "/node":    true,
		mcdBotA + "/uv":      true,
		mcdBotA + "/ripgrep": true,
		mcdBotC + "/codex":   true,
	})
	if got, _ := scanNullableText(t, pool, `SELECT operation_id FROM bot_dependency_installations WHERE bot_id = $1 AND dependency_id = 'node'`, mcdBotA); got != mcdOperationID {
		t.Errorf("node operation_id after rollback = %q", got)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'bot_dependency_installations' AND policyname LIKE 'bot_dependency_installations_team_%'`); n != 4 {
		t.Errorf("bot_dependency_installations policies after rollback = %d, want 4", n)
	}
	if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.bot_dependency_installations'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("rollback lost tenant isolation: enabled=%t forced=%t err=%v", enabled, forced, err)
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// And up again converges to the same state.
	migrateTo(t, dsn, 170)
	if got := columnOrder(t, pool, "managed_computer_dependencies"); got != wantColumns {
		t.Fatalf("columns after re-upgrade = %q, want %q", got, wantColumns)
	}
	assertSameKeys(t, "managed_computer_dependencies after re-upgrade", dependencyKeys(t, pool, "managed_computer_dependencies", "workspace_id"), wantKeys)
	again := constraintNames(t, pool, "managed_computer_dependencies")
	for name := range wantConstraints {
		if !again[name] {
			t.Errorf("re-upgrade lacks constraint %s", name)
		}
	}
}
