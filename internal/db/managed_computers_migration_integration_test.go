//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

// Fixture ids for the 0168 boundary. Team one is the default team; team two
// proves the backfill spans teams. C is a bot created after 0167: its managed
// workspace id is not the bot id, so every mapping must go through the
// bindings.
const (
	mcTeamTwo = "00000000-0000-0000-0000-0000000001f3"

	mcUserOne = "10000000-0000-4000-8000-000000000168"
	mcUserTwo = "10000000-0000-4000-8000-000000000169"

	mcBotA = "20000000-0000-4000-8000-00000000018a" // slot + container + snapshot/version/event + limits
	mcBotB = "20000000-0000-4000-8000-00000000018b" // slot only, desired absent
	mcBotC = "20000000-0000-4000-8000-00000000018c" // team two, workspace id != bot id, container + limits
	mcBotD = "20000000-0000-4000-8000-00000000018d" // container but no managed binding; its rows are orphans
	mcBotE = "20000000-0000-4000-8000-00000000018e" // team two, container but no slot

	mcWorkspaceC = "30000000-0000-4000-8000-00000000018c" // C's managed computer

	mcSnapshotA      = "40000000-0000-4000-8000-00000000018a"
	mcSnapshotOrphan = "40000000-0000-4000-8000-0000000001ff" // taken on D's container
	mcVersionA       = "41000000-0000-4000-8000-00000000018a"
)

func seedManagedComputerFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO teams (id, slug) VALUES ($1, 'mc-team-two')`, []any{mcTeamTwo}},
		{`INSERT INTO users (id, username) VALUES ($1, 'mc-owner-one'), ($2, 'mc-owner-two')`, []any{mcUserOne, mcUserTwo}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($3, $4)`, []any{team.DefaultTeamID, mcUserOne, mcTeamTwo, mcUserTwo}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name, status) VALUES
			($1, $6, $7, 'mc-bot-a', 'ready'),
			($2, $6, $7, 'mc-bot-b', 'ready'),
			($4, $6, $7, 'mc-bot-d', 'ready'),
			($3, $8, $9, 'mc-bot-c', 'ready'),
			($5, $8, $9, 'mc-bot-e', 'ready')`,
			[]any{mcBotA, mcBotB, mcBotC, mcBotD, mcBotE, team.DefaultTeamID, mcUserOne, mcTeamTwo, mcUserTwo}},
		// Managed workspaces: A, B and E keep the bot id (0167 backfill); C
		// was created after 0167 and has its own id.
		{`INSERT INTO workspaces (id, team_id, owner_user_id, kind, name) VALUES
			($1, $5, $6, 'managed', 'mc-bot-a'),
			($2, $5, $6, 'managed', 'mc-bot-b'),
			($3, $7, $8, 'managed', 'mc-bot-c'),
			($4, $7, $8, 'managed', 'mc-bot-e')`,
			[]any{mcBotA, mcBotB, mcWorkspaceC, mcBotE, team.DefaultTeamID, mcUserOne, mcTeamTwo, mcUserTwo}},
		{`INSERT INTO bot_workspace_bindings (team_id, bot_id, workspace_id, is_primary) VALUES
			($1, $2, $2, true),
			($1, $3, $3, true),
			($4, $5, $6, true),
			($4, $7, $7, true)`,
			[]any{team.DefaultTeamID, mcBotA, mcBotB, mcTeamTwo, mcBotC, mcWorkspaceC, mcBotE}},
		{`INSERT INTO bot_workspaces (bot_id, team_id, desired_state, desired_generation, image, observed_state, observed_generation, ever_ready, version, last_error_code) VALUES
			($1, $4, 'present', 3, 'memoh/workspace:a', 'running', 3, true, 5, ''),
			($2, $4, 'absent', 2, '', 'absent', 2, false, 3, ''),
			($3, $5, 'present', 1, '', 'failed', 1, false, 2, 'workspace.setup_failed')`,
			[]any{mcBotA, mcBotB, mcBotC, team.DefaultTeamID, mcTeamTwo}},
		// D has a container record but no managed binding: nothing can own
		// its rows after the upgrade.
		{`INSERT INTO containers (team_id, bot_id, container_id, container_name, image, status, namespace, last_started_at) VALUES
			($1, $2, 'ctr-mc-a', 'workspace-mc-a', 'memoh/workspace:a-run', 'running', 'memoh', now()),
			($1, $6, 'ctr-mc-d', 'workspace-mc-d', 'memoh/workspace:d', 'stopped', 'default', NULL),
			($3, $4, 'ctr-mc-c', 'workspace-mc-c', 'memoh/workspace:c', 'stopped', 'default', NULL),
			($3, $5, 'ctr-mc-e', 'workspace-mc-e', 'memoh/workspace:e', 'running', 'default', now())`,
			[]any{team.DefaultTeamID, mcBotA, mcTeamTwo, mcBotC, mcBotE, mcBotD}},
		{`INSERT INTO snapshots (id, team_id, container_id, runtime_snapshot_name, display_name, snapshotter, source) VALUES
			($1, $3, 'ctr-mc-a', 'ctr-mc-a-snapshot-1', 'before upgrade', 'overlayfs', 'manual'),
			($2, $3, 'ctr-mc-d', 'ctr-mc-d-snapshot-1', NULL, 'overlayfs', 'manual')`,
			[]any{mcSnapshotA, mcSnapshotOrphan, team.DefaultTeamID}},
		{`INSERT INTO container_versions (id, team_id, container_id, snapshot_id, version)
			VALUES ($1, $2, 'ctr-mc-a', $3, 1)`, []any{mcVersionA, team.DefaultTeamID, mcSnapshotA}},
		{`INSERT INTO lifecycle_events (id, team_id, container_id, event_type, payload) VALUES
			('ctr-mc-a-1', $1, 'ctr-mc-a', 'snapshot_create', '{"version":1}'::jsonb),
			('ctr-mc-d-1', $1, 'ctr-mc-d', 'snapshot_create', '{}'::jsonb)`, []any{team.DefaultTeamID}},
		{`INSERT INTO bot_workspace_resource_limits (bot_id, team_id, cpu_millicores, memory_bytes, storage_bytes) VALUES
			($1, $4, 1000, 2048, 0),
			($2, $5, 500, 0, 4096),
			($3, $4, 250, 0, 0)`,
			[]any{mcBotA, mcBotC, mcBotD, team.DefaultTeamID, mcTeamTwo}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed pre-0168 fixture: %v\n%s", err, stmt.sql)
		}
	}
}

type managedComputerRow struct {
	team, desired, observed, image, instanceID, instanceName, instanceStatus, namespace string
	desiredGeneration, version                                                          int64
	everReady                                                                           bool
}

func readManagedComputers(t *testing.T, pool *pgxpool.Pool) map[string]managedComputerRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT workspace_id, team_id, desired_state, observed_state, image, instance_id, instance_name,
		       instance_status, namespace, desired_generation, version, ever_ready
		  FROM managed_computers`)
	if err != nil {
		t.Fatalf("list managed computers: %v", err)
	}
	defer rows.Close()
	out := map[string]managedComputerRow{}
	for rows.Next() {
		var id string
		var r managedComputerRow
		if err := rows.Scan(&id, &r.team, &r.desired, &r.observed, &r.image, &r.instanceID, &r.instanceName,
			&r.instanceStatus, &r.namespace, &r.desiredGeneration, &r.version, &r.everReady); err != nil {
			t.Fatalf("scan managed computer: %v", err)
		}
		out[id] = r
	}
	return out
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func tableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatalf("to_regclass(%s): %v", table, err)
	}
	return exists
}

func TestManagedComputersMigrationBackfillsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	migrateTo(t, dsn, 167)
	seedManagedComputerFixture(t, pool)

	migrateTo(t, dsn, 168)

	for _, table := range []string{"containers", "bot_workspaces", "bot_workspace_resource_limits", "snapshots", "container_versions", "lifecycle_events"} {
		if tableExists(t, pool, table) {
			t.Fatalf("%s still exists after 0168", table)
		}
	}

	// One computer per managed workspace that had a slot or a container: A
	// (slot + latest container), B (slot, no container), C (slot + container
	// under its own workspace id), E (container only, 0153 rule). D has none.
	computers := readManagedComputers(t, pool)
	want := map[string]managedComputerRow{
		mcBotA:       {team.DefaultTeamID, "present", "running", "memoh/workspace:a", "ctr-mc-a", "workspace-mc-a", "running", "memoh", 3, 5, true},
		mcBotB:       {team.DefaultTeamID, "absent", "absent", "", "", "", "", "default", 2, 3, false},
		mcWorkspaceC: {mcTeamTwo, "present", "failed", "memoh/workspace:c", "ctr-mc-c", "workspace-mc-c", "stopped", "default", 1, 2, false},
		mcBotE:       {mcTeamTwo, "present", "running", "memoh/workspace:e", "ctr-mc-e", "workspace-mc-e", "running", "default", 1, 1, true},
	}
	if len(computers) != len(want) {
		t.Fatalf("managed_computers = %v, want %v", computers, want)
	}
	for id, w := range want {
		if computers[id] != w {
			t.Errorf("managed computer %s = %+v, want %+v", id, computers[id], w)
		}
	}
	if _, ok := computers[mcBotC]; ok {
		t.Fatal("C's computer must be keyed by its workspace id, not the bot id")
	}
	var lastErrorCode string
	if err := pool.QueryRow(ctx, `SELECT last_error_code FROM managed_computers WHERE workspace_id = $1`, mcWorkspaceC).Scan(&lastErrorCode); err != nil || lastErrorCode != "workspace.setup_failed" {
		t.Errorf("C last_error_code = %q err=%v, want the slot's code", lastErrorCode, err)
	}

	// Child rows follow container_id -> bot -> managed workspace; rows of a
	// bot without a managed binding (D) are orphans and deleted, and D gets
	// no computer.
	var snapshotWorkspace, versionWorkspace, eventWorkspace string
	if err := pool.QueryRow(ctx, `SELECT workspace_id FROM managed_computer_snapshots WHERE id = $1`, mcSnapshotA).Scan(&snapshotWorkspace); err != nil || snapshotWorkspace != mcBotA {
		t.Errorf("snapshot workspace = %q err=%v, want %s", snapshotWorkspace, err, mcBotA)
	}
	if err := pool.QueryRow(ctx, `SELECT workspace_id FROM managed_computer_versions WHERE id = $1`, mcVersionA).Scan(&versionWorkspace); err != nil || versionWorkspace != mcBotA {
		t.Errorf("version workspace = %q err=%v, want %s", versionWorkspace, err, mcBotA)
	}
	if err := pool.QueryRow(ctx, `SELECT workspace_id FROM managed_computer_events WHERE id = 'ctr-mc-a-1'`).Scan(&eventWorkspace); err != nil || eventWorkspace != mcBotA {
		t.Errorf("event workspace = %q err=%v, want %s", eventWorkspace, err, mcBotA)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computer_snapshots`); n != 1 {
		t.Errorf("snapshots after upgrade = %d, want 1 (the orphan is deleted)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computers WHERE instance_id = 'ctr-mc-d'`); n != 0 {
		t.Errorf("D's container became a computer without a binding: %d rows", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computer_events`); n != 1 {
		t.Errorf("events after upgrade = %d, want 1 (the orphan is deleted)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computer_versions`); n != 1 {
		t.Errorf("versions after upgrade = %d, want 1", n)
	}
	var snapshotContainer string
	if err := pool.QueryRow(ctx, `SELECT container_id FROM managed_computer_snapshots WHERE id = $1`, mcSnapshotA).Scan(&snapshotContainer); err != nil || snapshotContainer != "ctr-mc-a" {
		t.Errorf("snapshot container_id = %q err=%v, want the instance it was taken on", snapshotContainer, err)
	}

	// Resource limits are keyed by the computer; D's have no computer.
	type limits struct {
		cpu, memory, storage int64
	}
	readLimits := func() map[string]limits {
		rows, err := pool.Query(ctx, `SELECT workspace_id, cpu_millicores, memory_bytes, storage_bytes FROM managed_computer_resource_limits`)
		if err != nil {
			t.Fatalf("list limits: %v", err)
		}
		defer rows.Close()
		out := map[string]limits{}
		for rows.Next() {
			var id string
			var l limits
			if err := rows.Scan(&id, &l.cpu, &l.memory, &l.storage); err != nil {
				t.Fatalf("scan limits: %v", err)
			}
			out[id] = l
		}
		return out
	}
	gotLimits := readLimits()
	wantLimits := map[string]limits{mcBotA: {1000, 2048, 0}, mcWorkspaceC: {500, 0, 4096}}
	if len(gotLimits) != len(wantLimits) {
		t.Fatalf("limits = %v, want %v", gotLimits, wantLimits)
	}
	for id, w := range wantLimits {
		if gotLimits[id] != w {
			t.Errorf("limits %s = %+v, want %+v", id, gotLimits[id], w)
		}
	}

	for _, table := range []string{"managed_computers", "managed_computer_snapshots", "managed_computer_versions", "managed_computer_events", "managed_computer_resource_limits"} {
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("%s tenant isolation: enabled=%t forced=%t err=%v", table, enabled, forced, err)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = $1`, table); n != 4 {
			t.Fatalf("%s has %d policies, want 4", table, n)
		}
	}
	if target := fkTargetTable(t, pool, "managed_computer_snapshots", "managed_computer_snapshots_workspace_id_fkey"); target != "managed_computers" {
		t.Fatalf("snapshot workspace key targets %s", target)
	}
	if target := fkTargetTable(t, pool, "managed_computer_versions", "managed_computer_versions_snapshot_id_fkey"); target != "managed_computer_snapshots" {
		t.Fatalf("version snapshot key targets %s", target)
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// A snapshot cannot point at a computer that does not exist.
	if _, err := pool.Exec(ctx, `INSERT INTO managed_computer_snapshots (team_id, workspace_id, container_id, runtime_snapshot_name, snapshotter, source)
		VALUES ($1, $2, 'ctr-x', 'ctr-x-snapshot', 'overlayfs', 'manual')`, team.DefaultTeamID, "30000000-0000-4000-8000-0000000001ff"); err == nil {
		t.Fatal("a snapshot pointing at a missing computer must be rejected")
	}
	// Deleting the workspace cascades to the computer and its rows.
	if _, err := pool.Exec(ctx, `DELETE FROM bot_workspace_bindings WHERE workspace_id = $1`, mcBotE); err != nil {
		t.Fatalf("unbind E: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, mcBotE); err != nil {
		t.Fatalf("delete E's workspace: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computers WHERE workspace_id = $1`, mcBotE); n != 0 {
		t.Fatalf("E's computer survived its workspace: %d rows", n)
	}

	// Re-running the upgrade is harmless.
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0168_managed_computers.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("repeat 0168 up: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM managed_computers`); n != 3 {
		t.Fatalf("computers after repeat = %d, want 3", n)
	}

	// Down restores the bot-keyed tables from the bindings: every computer
	// goes back to its first bot, instances become containers rows, child
	// rows hang off containers(container_id) again.
	migrateTo(t, dsn, 167)
	for _, table := range []string{"managed_computers", "managed_computer_snapshots", "managed_computer_versions", "managed_computer_events", "managed_computer_resource_limits"} {
		if tableExists(t, pool, table) {
			t.Fatalf("%s still exists after rollback", table)
		}
	}
	type slot struct {
		team, desired, observed, image string
		generation                     int64
	}
	slotRows, err := pool.Query(ctx, `SELECT bot_id, team_id, desired_state, observed_state, image, desired_generation FROM bot_workspaces`)
	if err != nil {
		t.Fatalf("list restored slots: %v", err)
	}
	gotSlots := map[string]slot{}
	for slotRows.Next() {
		var id string
		var s slot
		if err := slotRows.Scan(&id, &s.team, &s.desired, &s.observed, &s.image, &s.generation); err != nil {
			t.Fatalf("scan slot: %v", err)
		}
		gotSlots[id] = s
	}
	slotRows.Close()
	wantSlots := map[string]slot{
		mcBotA: {team.DefaultTeamID, "present", "running", "memoh/workspace:a", 3},
		mcBotB: {team.DefaultTeamID, "absent", "absent", "", 2},
		mcBotC: {mcTeamTwo, "present", "failed", "memoh/workspace:c", 1},
	}
	if len(gotSlots) != len(wantSlots) {
		t.Fatalf("restored bot_workspaces = %v, want %v", gotSlots, wantSlots)
	}
	for id, w := range wantSlots {
		if gotSlots[id] != w {
			t.Errorf("restored slot %s = %+v, want %+v", id, gotSlots[id], w)
		}
	}
	type containerRow struct {
		team, containerID, status, image, backend string
	}
	containerRows, err := pool.Query(ctx, `SELECT bot_id, team_id, container_id, status, image, workspace_backend FROM containers`)
	if err != nil {
		t.Fatalf("list restored containers: %v", err)
	}
	gotContainers := map[string]containerRow{}
	for containerRows.Next() {
		var id string
		var c containerRow
		if err := containerRows.Scan(&id, &c.team, &c.containerID, &c.status, &c.image, &c.backend); err != nil {
			t.Fatalf("scan container: %v", err)
		}
		gotContainers[id] = c
	}
	containerRows.Close()
	wantContainers := map[string]containerRow{
		mcBotA: {team.DefaultTeamID, "ctr-mc-a", "running", "memoh/workspace:a", "container"},
		mcBotC: {mcTeamTwo, "ctr-mc-c", "stopped", "memoh/workspace:c", "container"},
	}
	if len(gotContainers) != len(wantContainers) {
		t.Fatalf("restored containers = %v, want %v", gotContainers, wantContainers)
	}
	for id, w := range wantContainers {
		if gotContainers[id] != w {
			t.Errorf("restored container %s = %+v, want %+v", id, gotContainers[id], w)
		}
	}
	var restoredSnapshotContainer string
	if err := pool.QueryRow(ctx, `SELECT container_id FROM snapshots WHERE id = $1`, mcSnapshotA).Scan(&restoredSnapshotContainer); err != nil || restoredSnapshotContainer != "ctr-mc-a" {
		t.Errorf("restored snapshot = %q err=%v", restoredSnapshotContainer, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM container_versions WHERE container_id = 'ctr-mc-a'`); n != 1 {
		t.Errorf("restored versions = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM lifecycle_events WHERE container_id = 'ctr-mc-a'`); n != 1 {
		t.Errorf("restored events = %d, want 1", n)
	}
	if target := fkTargetTable(t, pool, "snapshots", "snapshots_container_id_fkey"); target != "containers" {
		t.Fatalf("after rollback snapshots_container_id_fkey targets %s", target)
	}
	var limitsBots string
	if err := pool.QueryRow(ctx, `SELECT string_agg(bot_id::text, ',' ORDER BY bot_id) FROM bot_workspace_resource_limits`).Scan(&limitsBots); err != nil || limitsBots != mcBotA+","+mcBotC {
		t.Errorf("restored limits bots = %q err=%v, want A and C", limitsBots, err)
	}
	var columns string
	if err := pool.QueryRow(ctx, `
		SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
		  FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='containers'`).Scan(&columns); err != nil {
		t.Fatalf("read restored columns: %v", err)
	}
	if columns != "id,bot_id,container_id,container_name,image,status,namespace,auto_start,container_path,workspace_backend,created_at,updated_at,last_started_at,last_stopped_at,team_id" {
		t.Fatalf("restored containers column order = %q", columns)
	}
	for _, table := range []string{"containers", "bot_workspaces", "bot_workspace_resource_limits", "snapshots", "container_versions", "lifecycle_events"} {
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("rollback lost tenant isolation on %s: enabled=%t forced=%t err=%v", table, enabled, forced, err)
		}
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)

	// And up again converges to the same state.
	migrateTo(t, dsn, 168)
	computers = readManagedComputers(t, pool)
	if len(computers) != 3 || computers[mcWorkspaceC].instanceID != "ctr-mc-c" || computers[mcBotA].version != 5 {
		t.Fatalf("computers after re-upgrade = %v", computers)
	}
}
