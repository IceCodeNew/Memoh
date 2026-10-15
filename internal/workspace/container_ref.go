package workspace

import (
	"context"

	ctr "github.com/felinics/memoh/internal/container"
)

// lockedContainerRef holds a per-container lock together with the latest
// container metadata so mutation workflows do not have to repeat the same
// resolve -> lock -> load sequence. workspaceID is the managed computer the
// container belongs to; botID the bot it is operated for (empty for a
// computer nothing is bound to).
type lockedContainerRef struct {
	manager     *Manager
	workspaceID string
	botID       string
	containerID string
	info        ctr.ContainerInfo
	unlock      func()
}

// loadLockedContainer resolves the bot's managed computer and locks its
// container. ErrContainerNotFound (which also satisfies ctr.IsNotFound) when
// the bot has no computer or the computer has no container.
func (m *Manager) loadLockedContainer(ctx context.Context, botID string) (*lockedContainerRef, error) {
	workspaceID, containerID, err := m.resolveBotContainer(ctx, botID)
	if err != nil {
		return nil, err
	}
	return m.lockContainerRef(ctx, workspaceID, botID, containerID)
}

// lockContainerRef locks an already resolved container and loads it.
func (m *Manager) lockContainerRef(ctx context.Context, workspaceID, botID, containerID string) (*lockedContainerRef, error) {
	unlock := m.lockContainer(containerID)

	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		unlock()
		return nil, err
	}

	return &lockedContainerRef{
		manager:     m,
		workspaceID: workspaceID,
		botID:       botID,
		containerID: containerID,
		info:        info,
		unlock:      unlock,
	}, nil
}

func (r *lockedContainerRef) Close() {
	if r == nil || r.unlock == nil {
		return
	}
	r.unlock()
	r.unlock = nil
}

func (r *lockedContainerRef) StopTaskForMutation(ctx context.Context) (func(), error) {
	return r.manager.stopTaskForMutation(ctx, r.workspaceID, r.botID, r.containerID)
}

// EnsureDBRecords records the container as the computer's instance.
func (r *lockedContainerRef) EnsureDBRecords(ctx context.Context) error {
	return r.manager.ensureDBRecords(ctx, r.workspaceID, r.botID, r.info.ID, r.info.Image)
}
