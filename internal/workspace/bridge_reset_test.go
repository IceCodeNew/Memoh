package workspace

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	bridgeResetComputerA = "cccccccc-cccc-4ccc-8ccc-00000000000a"
	bridgeResetComputerB = "cccccccc-cccc-4ccc-8ccc-00000000000b"
	bridgeResetBotA1     = "bbbbbbbb-bbbb-4bbb-8bbb-0000000000a1"
	bridgeResetBotA2     = "bbbbbbbb-bbbb-4bbb-8bbb-0000000000a2"
	bridgeResetBotB1     = "bbbbbbbb-bbbb-4bbb-8bbb-0000000000b1"
)

// newBridgeResetTestManager binds two bots to computer A and one to computer
// B; computer C has no bot.
func newBridgeResetTestManager() *Manager {
	m := &Manager{
		grpcPool: bridge.NewPool(func(string) string { return "" }),
		logger:   slog.New(slog.DiscardHandler),
	}
	store := &fakeWorkspaceStore{
		workspaces: []dbstore.WorkspaceRecord{
			{ID: bridgeResetComputerA, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindManaged, Name: "a"},
			{ID: bridgeResetComputerB, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindManaged, Name: "b"},
		},
		bindings: []dbstore.BotWorkspaceBindingRecord{
			{ID: "dddddddd-dddd-4ddd-8ddd-000000000001", BotID: bridgeResetBotA1, WorkspaceID: bridgeResetComputerA, IsPrimary: true},
			{ID: "dddddddd-dddd-4ddd-8ddd-000000000002", BotID: bridgeResetBotA2, WorkspaceID: bridgeResetComputerA, IsPrimary: true},
			{ID: "dddddddd-dddd-4ddd-8ddd-000000000003", BotID: bridgeResetBotB1, WorkspaceID: bridgeResetComputerB, IsPrimary: true},
		},
	}
	m.SetBindingService(NewBindingService(store, store))
	return m
}

func TestResetBridgeNotifiesEveryBoundBotInOrder(t *testing.T) {
	m := newBridgeResetTestManager()
	ctx := context.Background()

	var calls []string
	m.OnBridgeReset(func(botID string) { calls = append(calls, "first:"+botID) })
	m.OnBridgeReset(func(botID string) { calls = append(calls, "second:"+botID) })
	m.OnBridgeReset(nil)

	m.resetBridge(ctx, bridgeResetComputerA, ContainerPrefix+bridgeResetComputerA)
	m.resetBridge(ctx, bridgeResetComputerB, ContainerPrefix+bridgeResetComputerB)

	// A computer shared by two bots resets both; the subscribers run in
	// registration order for each bot.
	want := []string{
		"first:" + bridgeResetBotA1, "second:" + bridgeResetBotA1,
		"first:" + bridgeResetBotA2, "second:" + bridgeResetBotA2,
		"first:" + bridgeResetBotB1, "second:" + bridgeResetBotB1,
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("callbacks = %v, want %v", calls, want)
	}
}

func TestResetBridgeWithoutBoundBotsNotifiesNobody(t *testing.T) {
	m := newBridgeResetTestManager()

	var calls []string
	m.OnBridgeReset(func(botID string) { calls = append(calls, botID) })

	const unbound = "cccccccc-cccc-4ccc-8ccc-00000000000c"
	m.resetBridge(context.Background(), unbound, ContainerPrefix+unbound)
	if len(calls) != 0 {
		t.Fatalf("callbacks for a computer without bots = %v", calls)
	}

	// A manager without a binding service predates managed computers: the
	// computer is the bot, which is notified as such.
	m.SetBindingService(nil)
	m.resetBridge(context.Background(), bridgeResetComputerA, ContainerPrefix+bridgeResetComputerA)
	if !slices.Equal(calls, []string{bridgeResetComputerA}) {
		t.Fatalf("callbacks without bindings = %v, want the computer id as the bot", calls)
	}
}

func TestOnBridgeResetDoesNotReplayPastResets(t *testing.T) {
	m := newBridgeResetTestManager()
	ctx := context.Background()

	m.resetBridge(ctx, bridgeResetComputerA, ContainerPrefix+bridgeResetComputerA)

	var calls []string
	m.OnBridgeReset(func(botID string) { calls = append(calls, botID) })
	if len(calls) != 0 {
		t.Fatalf("callback replayed past reset: %v", calls)
	}

	m.resetBridge(ctx, bridgeResetComputerB, ContainerPrefix+bridgeResetComputerB)
	if !slices.Equal(calls, []string{bridgeResetBotB1}) {
		t.Fatalf("callbacks = %v, want [%s]", calls, bridgeResetBotB1)
	}
}

func TestResetBridgeCallbackMayRegisterAnother(t *testing.T) {
	m := newBridgeResetTestManager()

	var calls []string
	m.OnBridgeReset(func(botID string) {
		calls = append(calls, "outer:"+botID)
		// Registering from inside a callback must not deadlock; the new
		// callback only sees subsequent resets.
		m.OnBridgeReset(func(botID string) { calls = append(calls, "inner:"+botID) })
	})

	m.resetBridge(context.Background(), bridgeResetComputerB, ContainerPrefix+bridgeResetComputerB)
	if !slices.Equal(calls, []string{"outer:" + bridgeResetBotB1}) {
		t.Fatalf("callbacks after first reset = %v", calls)
	}
}

func TestResetBridgeNotifiesManagedSubscribersOncePerComputer(t *testing.T) {
	m := newBridgeResetTestManager()
	ctx := context.Background()

	var calls []string
	m.OnManagedBridgeReset(func(workspaceID string) { calls = append(calls, "computer:"+workspaceID) })
	m.OnBridgeReset(func(botID string) { calls = append(calls, "bot:"+botID) })
	m.OnManagedBridgeReset(nil)

	const unbound = "cccccccc-cccc-4ccc-8ccc-00000000000c"
	m.resetBridge(ctx, bridgeResetComputerA, ContainerPrefix+bridgeResetComputerA)
	m.resetBridge(ctx, unbound, ContainerPrefix+unbound)

	// The computer-keyed subscriber runs once per reset, before the bot-keyed
	// fan-out, and also for a computer nothing is bound to.
	want := []string{
		"computer:" + bridgeResetComputerA,
		"bot:" + bridgeResetBotA1, "bot:" + bridgeResetBotA2,
		"computer:" + unbound,
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("callbacks = %v, want %v", calls, want)
	}
}
