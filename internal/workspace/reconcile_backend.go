package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"

	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

// The Manager is the botworkspace.Backend for every native container runtime.
// The reconciler addresses a managed computer by its workspace id and so does
// every step here: the container is found by instance key, created as
// workspace-<workspace id>, and the bot bound to the computer is looked up
// once only for what is bot configuration (image preference, GPU, skill
// roots, display, network overlay, hooks). Each step is idempotent:
// startWorkspace reuses an existing container, PrepareImageForCreate skips a
// present image, teardownWorkspace treats a missing container as success.
var _ botworkspace.Backend = (*Manager)(nil)

// Provision implements botworkspace.Backend. image overrides the bot's
// resolved image when non-empty. Failures carry the phase and whether the
// reconciler should retry them. A computer nothing is bound to is not
// provisioned: its image, GPU and bootstrap are the bound bot's configuration.
func (m *Manager) Provision(ctx context.Context, workspaceID, image string, progress func(botworkspace.ProgressEvent)) error {
	botID, err := m.workspaceBot(ctx, workspaceID)
	if err != nil {
		return stepError(botworkspace.PhaseStart, err, !errors.Is(err, ErrWorkspaceUnbound))
	}
	emit := func(ev ContainerSetupEvent) {
		if progress != nil {
			progress(toProgressEvent(ev))
		}
	}
	return m.provisionWorkspace(ctx, workspaceID, botID, image, emit)
}

// Teardown implements botworkspace.Backend. An export that cannot succeed is
// not retried, so the failure reaches the user at once. The container is
// found by instance key, so a computer nothing is bound to is torn down like
// any other; the bot, when there is one, only selects the network overlay.
func (m *Manager) Teardown(ctx context.Context, workspaceID string, preserve bool) error {
	botID, err := m.workspaceBot(ctx, workspaceID)
	if err != nil && !errors.Is(err, ErrWorkspaceUnbound) {
		return err
	}
	err = m.teardownWorkspace(ctx, workspaceID, botID, preserve)
	if errors.Is(err, errPreserveData) {
		return stepError(botworkspace.PhaseTeardown, err, isTransient(err))
	}
	return err
}

// Inspect implements botworkspace.Backend.
func (m *Manager) Inspect(ctx context.Context, workspaceID string) (botworkspace.Inspection, error) {
	containerID, err := m.workspaceContainerID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, ErrContainerNotFound) {
			return botworkspace.Inspection{}, nil
		}
		return botworkspace.Inspection{}, err
	}
	info, err := m.service.GetContainer(ctx, containerID)
	if err != nil {
		if ctr.IsNotFound(err) {
			return botworkspace.Inspection{}, nil
		}
		return botworkspace.Inspection{}, err
	}
	return botworkspace.Inspection{
		Exists:  true,
		Running: m.isTaskRunning(ctx, containerID),
		Image:   config.NormalizeImageRef(strings.TrimSpace(info.Image)),
	}, nil
}

// provisionWorkspace runs the setup steps and attributes failures to phases.
// It is the only code path that creates a managed computer's workspace.
func (m *Manager) provisionWorkspace(ctx context.Context, workspaceID, botID, imageOverride string, emit func(ContainerSetupEvent)) error {
	image := strings.TrimSpace(imageOverride)
	if image == "" {
		resolved, err := m.resolveWorkspaceImage(ctx, botID)
		if err != nil {
			return stepError(botworkspace.PhaseImagePrepare, err, true)
		}
		image = resolved
	} else {
		image = config.NormalizeImageRef(image)
	}

	emit(ContainerSetupEvent{Type: "pulling", Image: image})
	result, err := m.PrepareImageForCreate(ctx, image, &ctr.PullImageOptions{
		Unpack:        true,
		StorageDriver: m.cfg.Snapshotter,
		OnProgress: func(p ctr.PullProgress) {
			emit(ContainerSetupEvent{Type: "pull_progress", Layers: p.Layers})
		},
	})
	if err != nil {
		// A registry that says the image does not exist will keep saying so;
		// only transport-level failures are worth retrying. The wrap adds the
		// image to the record and leaves the persisted message as it is.
		return stepError(botworkspace.PhaseImagePrepare, errs.Wrap(imagePullError(err), "", slog.String("image", image)), isTransient(err))
	}
	if strings.TrimSpace(result.ImageRef) != "" {
		image = result.ImageRef
	}
	switch result.Mode {
	case ImagePrepareSkipped:
		emit(ContainerSetupEvent{Type: "pull_skipped", Image: image, Message: result.Message})
	case ImagePrepareDelegated:
		emit(ContainerSetupEvent{Type: "pull_delegated", Image: image, Message: result.Message})
	}

	gpu, err := m.resolveWorkspaceGPU(ctx, botID)
	if err != nil {
		return stepError(botworkspace.PhaseStart, err, true)
	}

	emit(ContainerSetupEvent{Type: "creating"})
	hadPreservedData := m.preservedBackupPath(workspaceID, botID) != ""
	if hadPreservedData {
		emit(ContainerSetupEvent{Type: "restoring"})
	}
	containerID, err := m.startWorkspace(ctx, workspaceID, botID, image, gpu)
	if err != nil {
		return stepError(botworkspace.PhaseStart, err, true)
	}
	if err := m.waitForInstanceReady(ctx, workspaceID, containerID); err != nil {
		return stepError(botworkspace.PhaseBridge, err, true)
	}
	if err := m.initializeInstance(ctx, containerID); err != nil {
		// A template that cannot be written will not fix itself.
		return stepError(botworkspace.PhaseBootstrap, err, !errors.Is(err, ErrWorkspaceTemplateBootstrapFailed))
	}
	if err := m.RememberWorkspaceImage(ctx, botID, image); err != nil {
		record := errlog.Event(ctx, "workspace.provision", errs.Wrap(err, "remember workspace image", slog.String("image", image)), errlog.Options{})
		m.logger.LogAttrs(ctx, record.Level, "provision: remember workspace image failed", append([]slog.Attr{slog.String("bot_id", botID)}, record.Attrs()...)...)
	}

	m.recordInstance(ctx, workspaceID, containerID, "running", image)
	hasPreservedData := m.preservedBackupPath(workspaceID, botID) != ""
	event := ContainerSetupEvent{
		Type:             "complete",
		Image:            image,
		ContainerID:      containerID,
		WorkspaceBackend: workspaceBackendFromRecord(""),
		Snapshotter:      m.cfg.Snapshotter,
		Started:          true,
		DataRestored:     hadPreservedData && !hasPreservedData,
		HasPreservedData: hasPreservedData,
	}
	if status, err := m.workspaceStatus(ctx, workspaceID, botID); err == nil {
		event.ContainerID = status.ContainerID
		event.WorkspaceBackend = status.WorkspaceBackend
		event.RuntimeBackend = status.RuntimeBackend
		event.ContainerPath = status.ContainerPath
		event.CDIDevices = status.CDIDevices
		event.HasPreservedData = status.HasPreservedData
	}
	emit(event)
	return nil
}

func stepError(phase string, err error, retryable bool) error {
	// Budget exhaustion is always worth another attempt on a fresh budget.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		retryable = true
	}
	return &botworkspace.StepError{Phase: phase, Retryable: retryable, Err: err}
}

// imagePullError tags a failed image preparation with the conditions the user
// acts on differently: an image that does not exist or whose name is invalid,
// and a registry that cannot be reached. Any other failure is returned as is.
// The runtime backends report both through their container sentinels, so no
// error text is read.
func imagePullError(err error) error {
	switch {
	case errors.Is(err, ctr.ErrNotFound), errors.Is(err, ctr.ErrInvalidArgument):
		return fmt.Errorf("%w: %w", botworkspace.ErrImageNotFound, err)
	case isRegistryUnreachable(err):
		return fmt.Errorf("%w: %w", botworkspace.ErrImageRegistryUnavailable, err)
	}
	return err
}

// isRegistryUnreachable is the subset of transient failures that mean the
// remote end did not answer. A spent time budget is not one: it says nothing
// about the registry.
func isRegistryUnreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ctr.ErrUnavailable) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// isTransient classifies runtime errors that a later attempt may not see
// again: timeouts, network errors, an unreachable runtime, and control-plane
// conflicts such as an operation still in flight on the Cloud builtin backend.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	for _, target := range []error{
		context.DeadlineExceeded,
		context.Canceled,
		ctr.ErrConflict,
		ctr.ErrUnavailable,
		io.EOF,
		io.ErrUnexpectedEOF,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func toProgressEvent(ev ContainerSetupEvent) botworkspace.ProgressEvent {
	return botworkspace.ProgressEvent{
		Type:             ev.Type,
		Image:            ev.Image,
		Message:          ev.Message,
		Layers:           ev.Layers,
		ContainerID:      ev.ContainerID,
		WorkspaceBackend: ev.WorkspaceBackend,
		RuntimeBackend:   ev.RuntimeBackend,
		ContainerPath:    ev.ContainerPath,
		CDIDevices:       ev.CDIDevices,
		Snapshotter:      ev.Snapshotter,
		Started:          ev.Started,
		DataRestored:     ev.DataRestored,
		HasPreservedData: ev.HasPreservedData,
	}
}
