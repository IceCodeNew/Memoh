package workspace

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ErrWorkspaceUnbound: the managed computer has no bot bound to it. Containers
// are still named, networked and bootstrapped per bot in this stage, so a
// computer without a bot can only have its state recorded.
var ErrWorkspaceUnbound = errors.New("managed computer has no bound bot")

// SetManagedComputerStore wires the record store the manager keeps managed
// computers in. NewManager derives one from the connection pool; tests and
// the fx graph may supply their own.
func (m *Manager) SetManagedComputerStore(store dbstore.ManagedComputerStore) {
	m.computers = store
}

// SetBindingService wires the bot <-> workspace bindings the manager resolves
// bot ids through (setter injection: the binding service is built from the
// same stores the manager depends on).
func (m *Manager) SetBindingService(bindings *BindingService) {
	m.bindings = bindings
}

// managedWorkspaceID resolves a bot to its managed computer through the
// bindings. Every read or write of managed_computers and its child tables
// goes through here; the workspace id of a bot created after 0167 is not the
// bot id. A manager without bindings knows no managed computers and answers
// db.ErrNoManagedWorkspaceBinding.
func (m *Manager) managedWorkspaceID(ctx context.Context, botID string) (string, error) {
	if m.bindings == nil {
		return "", db.ErrNoManagedWorkspaceBinding
	}
	return m.bindings.ManagedWorkspaceID(ctx, botID)
}

// workspaceBot is the bot a managed computer is operated for: the first bot
// bound to it. ErrWorkspaceUnbound when there is none.
func (m *Manager) workspaceBot(ctx context.Context, workspaceID string) (string, error) {
	if m.bindings == nil {
		return "", ErrWorkspaceUnbound
	}
	bots, err := m.bindings.BoundBots(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if len(bots) == 0 {
		return "", ErrWorkspaceUnbound
	}
	return bots[0], nil
}

// managedComputer reads the bot's managed computer record. db.ErrNotFound
// when the computer has no row yet; db.ErrNoManagedWorkspaceBinding when the
// bot has no managed computer at all.
func (m *Manager) managedComputer(ctx context.Context, botID string) (dbstore.ManagedComputerRecord, error) {
	if m.computers == nil {
		return dbstore.ManagedComputerRecord{}, db.ErrNotFound
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		return dbstore.ManagedComputerRecord{}, err
	}
	return m.computers.GetManagedComputer(ctx, workspaceID)
}

// noManagedComputer reports the errors that mean "this bot has no managed
// computer record", which callers treat as an empty answer.
func noManagedComputer(err error) bool {
	return errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrNoManagedWorkspaceBinding)
}

// ---------------------------------------------------------------------------
// Instance record writes. The bot-keyed helpers resolve the computer first;
// the workspace-keyed ones are what the startup reconcile uses directly.
// ---------------------------------------------------------------------------

func (m *Manager) upsertContainerRecord(ctx context.Context, botID, containerID, status, image string) {
	if m.computers == nil {
		return
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		m.logger.WarnContext(ctx, "record container: no managed computer for bot",
			slog.String("bot_id", botID), slog.Any("error", err))
		return
	}
	m.recordInstance(ctx, workspaceID, containerID, status, image)
}

func (m *Manager) recordInstance(ctx context.Context, workspaceID, containerID, status, image string) {
	if m.computers == nil {
		return
	}
	ns := strings.TrimSpace(m.namespace)
	if ns == "" {
		ns = "default"
	}
	if err := m.computers.UpsertManagedComputerInstance(ctx, dbstore.ManagedComputerInstanceInput{
		WorkspaceID:   workspaceID,
		InstanceID:    containerID,
		InstanceName:  containerID,
		Image:         image,
		Status:        status,
		Namespace:     ns,
		AutoStart:     true,
		ContainerPath: config.DefaultDataMount,
	}); err != nil {
		m.logger.ErrorContext(ctx, "failed to record container instance",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
	}
	if status == "running" {
		m.recordInstanceStarted(ctx, workspaceID)
	}
}

func (m *Manager) deleteContainerRecord(ctx context.Context, botID string) {
	if m.computers == nil {
		return
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if !noManagedComputer(err) {
			m.logger.WarnContext(ctx, "clear container record: resolve managed computer failed",
				slog.String("bot_id", botID), slog.Any("error", err))
		}
		return
	}
	if err := m.computers.ClearManagedComputerInstance(ctx, workspaceID); err != nil {
		m.logger.ErrorContext(ctx, "failed to clear container instance",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
	}
}

func (m *Manager) markContainerStopped(ctx context.Context, botID string) {
	if workspaceID, ok := m.recordTarget(ctx, botID); ok {
		m.recordInstanceStopped(ctx, workspaceID)
	}
}

// recordTarget resolves the computer a bot-keyed status write goes to; false
// when the bot has none (nothing to record).
func (m *Manager) recordTarget(ctx context.Context, botID string) (string, bool) {
	if m.computers == nil {
		return "", false
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if !noManagedComputer(err) {
			m.logger.WarnContext(ctx, "record container status: resolve managed computer failed",
				slog.String("bot_id", botID), slog.Any("error", err))
		}
		return "", false
	}
	return workspaceID, true
}

func (m *Manager) recordInstanceStarted(ctx context.Context, workspaceID string) {
	if err := m.computers.MarkManagedComputerStarted(ctx, workspaceID); err != nil {
		m.logger.ErrorContext(ctx, "failed to record container started",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
	}
}

func (m *Manager) recordInstanceStopped(ctx context.Context, workspaceID string) {
	if err := m.computers.MarkManagedComputerStopped(ctx, workspaceID); err != nil {
		m.logger.ErrorContext(ctx, "failed to record container stopped",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
	}
}

func (m *Manager) recordInstanceStatus(ctx context.Context, workspaceID, status string) {
	if err := m.computers.UpdateManagedComputerInstanceStatus(ctx, workspaceID, status); err != nil {
		m.logger.ErrorContext(ctx, "failed to record container status",
			slog.String("workspace_id", workspaceID), slog.String("status", status), slog.Any("error", err))
	}
}
