package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type unreadableBridgeService struct{ legacyRouteTestService }

func (*unreadableBridgeService) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, errors.New("bridge refused")
}

// bindTestComputer gives the manager a binding service in which computerID
// is the managed computer of botID; the backend is addressed by the computer
// and operates the container named after the bot.
func bindTestComputer(m *Manager, botID, computerID string) {
	store := &fakeWorkspaceStore{
		workspaces: []dbstore.WorkspaceRecord{{ID: computerID, OwnerUserID: wsTestOwnerID, Kind: WorkspaceKindManaged, Name: "bot"}},
		bindings:   []dbstore.BotWorkspaceBindingRecord{{ID: wsTestBindingID, BotID: botID, WorkspaceID: computerID, IsPrimary: true}},
	}
	m.SetBindingService(NewBindingService(store, store))
}

func TestTeardownPreservingData(t *testing.T) {
	const botID = "00000000-0000-0000-0000-000000000001"
	const computerID = "00000000-0000-0000-0000-0000000000c1"
	svc := &unreadableBridgeService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{DataRoot: t.TempDir()})
	bindTestComputer(m, botID, computerID)

	// The container is already gone: nothing to export, the removal is done.
	if err := m.Teardown(context.Background(), computerID, true); err != nil {
		t.Fatalf("Teardown() of a missing container = %v", err)
	}

	svc.created = true
	deletes := svc.deleteCalls
	err := m.Teardown(context.Background(), computerID, true)
	var step *botworkspace.StepError
	if !errors.As(err, &step) || step.Retryable {
		t.Fatalf("Teardown() with a failing export = %v, want a non-retryable failure", err)
	}
	if svc.deleteCalls != deletes {
		t.Fatal("container deleted without its data exported")
	}
}

// A computer nothing is bound to has no bot to name its container after: it
// inspects as absent, tears down as already gone and cannot be provisioned.
func TestBackendUnboundComputer(t *testing.T) {
	const computerID = "00000000-0000-0000-0000-0000000000c2"
	svc := &legacyRouteTestService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{DataRoot: t.TempDir()})
	m.SetBindingService(NewBindingService(&fakeWorkspaceStore{}, &fakeWorkspaceStore{}))
	ctx := context.Background()

	if insp, err := m.Inspect(ctx, computerID); err != nil || insp.Exists {
		t.Fatalf("Inspect() = %+v, %v; want absent", insp, err)
	}
	if err := m.Teardown(ctx, computerID, true); err != nil {
		t.Fatalf("Teardown() = %v, want done", err)
	}
	err := m.Provision(ctx, computerID, "", nil)
	var step *botworkspace.StepError
	if !errors.As(err, &step) || step.Retryable || !errors.Is(err, ErrWorkspaceUnbound) {
		t.Fatalf("Provision() = %v, want a non-retryable ErrWorkspaceUnbound", err)
	}
	if svc.createCalls != 0 {
		t.Fatal("an unbound computer must not create a container")
	}
}

func TestIsTransientClassifiesByErrorChain(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"deadline":            {err: fmt.Errorf("pull: %w", context.DeadlineExceeded), want: true},
		"runtime conflict":    {err: errors.Join(ctr.ErrConflict, errors.New("operation in flight")), want: true},
		"runtime unavailable": {err: errors.Join(ctr.ErrUnavailable, ctr.ErrRuntime, errors.New("daemon down")), want: true},
		"registry transport": {
			err:  &url.Error{Op: "Get", URL: "https://registry.example/v2/", Err: &net.DNSError{Err: "no such host", Name: "registry.example", IsNotFound: true}},
			want: true,
		},
		"truncated stream":  {err: fmt.Errorf("read layer: %w", io.ErrUnexpectedEOF), want: true},
		"connection reset":  {err: errors.Join(ctr.ErrRuntime, os.NewSyscallError("read", syscall.ECONNRESET)), want: true},
		"connection refuse": {err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), want: true},
		"missing image": {
			err:  errors.Join(ctr.ErrNotFound, errors.New("manifest for memoh/whereof:latest not found")),
			want: false,
		},
		"runtime failure mentioning timeout": {
			err:  errors.Join(ctr.ErrRuntime, errors.New("invalid config: pull_timeout must be positive")),
			want: false,
		},
		"nil": {err: nil, want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := isTransient(tt.err); got != tt.want {
				t.Fatalf("isTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestImagePullErrorSeparatesMissingImageFromUnreachableRegistry(t *testing.T) {
	tests := map[string]struct {
		err  error
		want error
	}{
		"image missing":    {err: errors.Join(ctr.ErrNotFound, errors.New("registry: manifest unknown")), want: botworkspace.ErrImageNotFound},
		"invalid name":     {err: errors.Join(ctr.ErrInvalidArgument, errors.New("invalid reference format")), want: botworkspace.ErrImageNotFound},
		"runtime down":     {err: errors.Join(ctr.ErrUnavailable, ctr.ErrRuntime, errors.New("daemon down")), want: botworkspace.ErrImageRegistryUnavailable},
		"connection reset": {err: errors.Join(ctr.ErrRuntime, os.NewSyscallError("read", syscall.ECONNRESET)), want: botworkspace.ErrImageRegistryUnavailable},
		"dns failure": {
			err:  errors.Join(ctr.ErrRuntime, &url.Error{Op: "Get", URL: "https://registry.example/v2/", Err: &net.DNSError{Err: "no such host", Name: "registry.example", IsNotFound: true}}),
			want: botworkspace.ErrImageRegistryUnavailable,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := imagePullError(tt.err)
			if !errors.Is(got, tt.want) || !errors.Is(got, tt.err) {
				t.Fatalf("imagePullError = %v, want %v with the cause kept", got, tt.want)
			}
		})
	}
	for name, err := range map[string]error{
		"budget spent": fmt.Errorf("pull: %w", context.DeadlineExceeded),
		"canceled":     context.Canceled,
		"other":        errors.Join(ctr.ErrRuntime, errors.New("unpack failed")),
	} {
		t.Run(name, func(t *testing.T) {
			got := imagePullError(err)
			if errors.Is(got, botworkspace.ErrImageNotFound) || errors.Is(got, botworkspace.ErrImageRegistryUnavailable) {
				t.Fatalf("imagePullError(%v) = %v, want untagged", err, got)
			}
		})
	}
}
