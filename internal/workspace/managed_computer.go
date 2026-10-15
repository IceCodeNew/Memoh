package workspace

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ErrWorkspaceUnbound: the managed computer has no bot bound to it. Its
// container is addressed by instance key, so it can still be inspected and
// torn down; only steps that need bot configuration (image preference, GPU,
// skill roots, display, hooks) have nothing to go on.
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
// bindings. Every bot-keyed entry point resolves here once and continues with
// the workspace id; the workspace id of a bot created after 0167 is not the
// bot id. db.ErrNoManagedWorkspaceBinding when the bot has no managed
// computer.
//
// A manager without a binding service predates managed computers: every bot
// is its own computer, as it was before 0167, so the bot id is the workspace
// id and the computer's only bot is the bot itself.
func (m *Manager) managedWorkspaceID(ctx context.Context, botID string) (string, error) {
	if m.bindings == nil {
		if strings.TrimSpace(botID) == "" {
			return "", db.ErrNoManagedWorkspaceBinding
		}
		return botID, nil
	}
	return m.bindings.ManagedWorkspaceID(ctx, botID)
}

// workspaceBot is the bot a managed computer is operated for: the first bot
// bound to it. ErrWorkspaceUnbound when there is none.
func (m *Manager) workspaceBot(ctx context.Context, workspaceID string) (string, error) {
	bots, err := m.boundBotsErr(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if len(bots) == 0 {
		return "", ErrWorkspaceUnbound
	}
	return bots[0], nil
}

// boundBotsErr lists the bots bound to the computer. Without a binding
// service the computer is the bot (see managedWorkspaceID).
func (m *Manager) boundBotsErr(ctx context.Context, workspaceID string) ([]string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, nil
	}
	if m.bindings == nil {
		return []string{workspaceID}, nil
	}
	bots, err := m.bindings.BoundBots(ctx, workspaceID)
	if errors.Is(err, ErrWorkspaceNotFound) {
		return nil, nil
	}
	return bots, err
}

// boundBots is boundBotsErr for best-effort callers: a failed lookup is
// logged and treated as "no bots".
func (m *Manager) boundBots(ctx context.Context, workspaceID string) []string {
	bots, err := m.boundBotsErr(ctx, workspaceID)
	if err != nil {
		m.logger.WarnContext(ctx, "list bots bound to managed computer failed",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
		return nil
	}
	return bots
}

// containerWorkspaceID resolves a container in the runtime to the managed
// computer it belongs to: the workspace label, then the instance record, then
// the bot label or bot-named id through the bindings. False when none of them
// know the container.
func (m *Manager) containerWorkspaceID(ctx context.Context, info ctr.ContainerInfo) (string, bool) {
	if workspaceID, ok := WorkspaceIDFromContainerInfo(info); ok {
		return workspaceID, true
	}
	if m.computers != nil && strings.TrimSpace(info.ID) != "" {
		record, err := m.computers.GetManagedComputerByInstanceID(ctx, info.ID)
		if err == nil && strings.TrimSpace(record.WorkspaceID) != "" {
			return record.WorkspaceID, true
		}
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			m.logger.WarnContext(ctx, "resolve container record failed",
				slog.String("container_id", info.ID), slog.Any("error", err))
		}
	}
	botID, ok := BotIDFromContainerInfo(info)
	if !ok {
		return "", false
	}
	workspaceID, err := m.managedWorkspaceID(ctx, botID)
	if err != nil {
		if !noManagedComputer(err) {
			m.logger.WarnContext(ctx, "resolve container bot binding failed",
				slog.String("container_id", info.ID), slog.String("bot_id", botID), slog.Any("error", err))
		}
		return "", false
	}
	return workspaceID, true
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

// recordedInstance is the container id the computer's record names, empty
// when the computer has no row or no container.
func (m *Manager) recordedInstance(ctx context.Context, workspaceID string) (string, error) {
	if m.computers == nil {
		return "", nil
	}
	record, err := m.computers.GetManagedComputer(ctx, workspaceID)
	if errors.Is(err, db.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(record.InstanceID), nil
}

// noManagedComputer reports the errors that mean "this bot has no managed
// computer record", which callers treat as an empty answer.
func noManagedComputer(err error) bool {
	return errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrNoManagedWorkspaceBinding)
}

// ---------------------------------------------------------------------------
// Instance record writes. The bot-keyed helpers resolve the computer first;
// the workspace-keyed ones are what the internal paths use directly.
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

func (m *Manager) clearInstanceRecord(ctx context.Context, workspaceID string) {
	if m.computers == nil {
		return
	}
	if err := m.computers.ClearManagedComputerInstance(ctx, workspaceID); err != nil {
		m.logger.ErrorContext(ctx, "failed to clear container instance",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
	}
}

func (m *Manager) recordInstanceStarted(ctx context.Context, workspaceID string) {
	if err := m.computers.MarkManagedComputerStarted(ctx, workspaceID); err != nil {
		m.logger.ErrorContext(ctx, "failed to record container started",
			slog.String("workspace_id", workspaceID), slog.Any("error", err))
	}
}

func (m *Manager) recordInstanceStopped(ctx context.Context, workspaceID string) {
	if m.computers == nil {
		return
	}
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
