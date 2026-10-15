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
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

// The Manager is the botworkspace.Backend for every native container runtime.
// The reconciler addresses a managed computer by its workspace id; the
// container behind it is still named, networked and bootstrapped per bot in
// this stage, so each step resolves the bot bound to the computer first. Each
// step is idempotent: StartWithResolvedConfig reuses an existing container,
// PrepareImageForCreate skips a present image, CleanupBotContainer treats a
// missing container as success.
var _ botworkspace.Backend = (*Manager)(nil)

// Provision implements botworkspace.Backend. image overrides the bot's
// resolved image when non-empty. Failures carry the phase and whether the
// reconciler should retry them. A computer nothing is bound to cannot be
// provisioned: there is no bot to name and bootstrap the container for.
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
	return m.provisionWorkspace(ctx, botID, image, emit)
}

// Teardown implements botworkspace.Backend. An export that cannot succeed is
// not retried, so the failure reaches the user at once. A computer nothing is
// bound to is already gone when it has no recorded instance; one that still
// has a container needs a bot to export and remove it and is reported as is.
func (m *Manager) Teardown(ctx context.Context, workspaceID string, preserve bool) error {
	botID, err := m.workspaceBot(ctx, workspaceID)
	if err != nil {
		if !errors.Is(err, ErrWorkspaceUnbound) {
			return err
		}
		if !m.hasRecordedInstance(ctx, workspaceID) {
			return nil
		}
		return stepError(botworkspace.PhaseTeardown, err, false)
	}
	err = m.CleanupBotContainer(ctx, botID, preserve)
	if errors.Is(err, errPreserveData) {
		return stepError(botworkspace.PhaseTeardown, err, isTransient(err))
	}
	return err
}

// Inspect implements botworkspace.Backend.
func (m *Manager) Inspect(ctx context.Context, workspaceID string) (botworkspace.Inspection, error) {
	botID, err := m.workspaceBot(ctx, workspaceID)
	var containerID string
	switch {
	case err == nil:
		containerID = m.resolveContainerID(ctx, botID)
	case errors.Is(err, ErrWorkspaceUnbound):
		// Only the recorded instance can say what exists for a computer
		// without a bot.
		record, rerr := m.recordedInstance(ctx, workspaceID)
		if rerr != nil {
			return botworkspace.Inspection{}, rerr
		}
		if record == "" {
			return botworkspace.Inspection{}, nil
		}
		containerID = record
	default:
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

func (m *Manager) hasRecordedInstance(ctx context.Context, workspaceID string) bool {
	instance, err := m.recordedInstance(ctx, workspaceID)
	// A record that cannot be read is taken to still name a container.
	return err != nil || instance != ""
}

// provisionWorkspace runs the setup steps and attributes failures to phases.
// It is the only code path that creates a bot workspace.
func (m *Manager) provisionWorkspace(ctx context.Context, botID, imageOverride string, emit func(ContainerSetupEvent)) error {
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
	hadPreservedData := m.HasPreservedData(botID)
	if hadPreservedData {
		emit(ContainerSetupEvent{Type: "restoring"})
	}
	if err := m.StartWithResolvedConfig(ctx, botID, image, gpu); err != nil {
		return stepError(botworkspace.PhaseStart, err, true)
	}
	if err := m.WaitForWorkspaceReady(ctx, botID); err != nil {
		return stepError(botworkspace.PhaseBridge, err, true)
	}
	if err := m.InitializeNativeWorkspace(ctx, botID); err != nil {
		// A template that cannot be written will not fix itself.
		return stepError(botworkspace.PhaseBootstrap, err, !errors.Is(err, ErrWorkspaceTemplateBootstrapFailed))
	}
	if err := m.RememberWorkspaceImage(ctx, botID, image); err != nil {
		record := errlog.Event(ctx, "workspace.provision", errs.Wrap(err, "remember workspace image", slog.String("image", image)), errlog.Options{})
		m.logger.LogAttrs(ctx, record.Level, "provision: remember workspace image failed", append([]slog.Attr{slog.String("bot_id", botID)}, record.Attrs()...)...)
	}

	containerID := m.resolveContainerID(ctx, botID)
	m.upsertContainerRecord(ctx, botID, containerID, "running", image)
	event := ContainerSetupEvent{
		Type:             "complete",
		Image:            image,
		ContainerID:      containerID,
		WorkspaceBackend: workspaceBackendFromRecord(""),
		Snapshotter:      m.cfg.Snapshotter,
		Started:          true,
		DataRestored:     hadPreservedData && !m.HasPreservedData(botID),
		HasPreservedData: m.HasPreservedData(botID),
	}
	if status, err := m.GetContainerInfo(ctx, botID); err == nil {
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
