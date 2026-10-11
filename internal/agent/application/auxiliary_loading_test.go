package application

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type unavailableWorkspaceTarget struct {
	target workspace.ResolvedWorkspaceTarget
	err    error
}

func (r unavailableWorkspaceTarget) ResolveWorkspaceTarget(context.Context, string, string) (workspace.ResolvedWorkspaceTarget, error) {
	return r.target, r.err
}

func TestUnavailableWorkspacePreservesPinnedTargetAndPermissionBoundary(t *testing.T) {
	service := workdirBoundService(workdir.Resolved{WorkdirID: "p1", TargetID: "computer-b", Kind: workdir.TargetKindRemote, WorkDir: "/Users/alice/code"})
	service.workspaceTargets = unavailableWorkspaceTarget{target: workspace.ResolvedWorkspaceTarget{TargetID: "computer-b", Kind: workspace.WorkspaceTargetRemote, Name: "Computer B"}, err: workspace.ErrCapabilityUnavailable}
	service.botPermissions = workspaceRequestPermission(true)
	req := ChatRequest{BotID: "bot-1", ThreadID: "s1", UserID: "user-1"}
	ctx, got, err := service.prepareWorkspaceRequest(t.Context(), req)
	if err != nil || !bridge.WorkspaceUnavailableFromContext(ctx) || workspace.WorkspaceTargetFromContext(ctx) != "computer-b" || got.WorkspaceTarget.Name != "Computer B" {
		t.Fatalf("request=%+v err=%v", got, err)
	}
	service.botPermissions = workspaceRequestPermission(false)
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), req); err == nil {
		t.Fatal("offline target bypassed workspace permission")
	}
	req.WorkspaceTargetID = "native"
	if _, _, err := service.prepareWorkspaceRequest(t.Context(), req); !errors.Is(err, ErrWorkspaceTargetWorkdirConflict) {
		t.Fatalf("pinned target conflict bypassed: %v", err)
	}
}

func TestWorkspaceFailuresOnlyDegradeWithKnownIdentityAndCapabilityError(t *testing.T) {
	for _, scenario := range []struct {
		name, id string
		err      error
	}{{"database", "native", errors.New("database unavailable")}, {"unknown identity", "", workspace.ErrCapabilityUnavailable}, {"missing target", "native", workspace.ErrWorkspaceTargetNotFound}} {
		t.Run(scenario.name, func(t *testing.T) {
			s := &Service{workspaceTargets: unavailableWorkspaceTarget{target: workspace.ResolvedWorkspaceTarget{TargetID: scenario.id}, err: scenario.err}}
			if _, _, err := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"}); !errors.Is(err, scenario.err) {
				t.Fatalf("error=%v want %v", err, scenario.err)
			}
		})
	}
}

func TestLazyBridgeCannotRegisterToolsForUnavailableWorkspace(t *testing.T) {
	client, err := bridge.Dial(t.Context(), "unix://"+t.TempDir()+"/missing.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	s := &Service{workspaceTargets: unavailableWorkspaceTarget{target: workspace.ResolvedWorkspaceTarget{TargetID: "native", Kind: workspace.WorkspaceTargetNative, Client: client}}}
	ctx, req, err := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"})
	if err != nil || req.WorkspaceTargetID != "native" || !bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatalf("req=%+v err=%v", req, err)
	}
}

type providerTimeoutBridge struct {
	pb.UnimplementedContainerServiceServer
}

func (providerTimeoutBridge) Stat(context.Context, *pb.StatRequest) (*pb.StatResponse, error) {
	return nil, status.Error(codes.DeadlineExceeded, "sandbox provider timed out")
}

// A timeout the workspace provider reports is an explicit failure: the turn
// degrades to text-only instead of registering tools for a stalled workspace.
func TestProviderTimeoutDegradesWorkspaceProbe(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, providerTimeoutBridge{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := bridge.NewClientFromConn(conn)
	defer func() { _ = client.Close() }()
	s := &Service{workspaceTargets: unavailableWorkspaceTarget{target: workspace.ResolvedWorkspaceTarget{TargetID: "native", Kind: workspace.WorkspaceTargetNative, Client: client}}}
	ctx, req, err := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"})
	if err != nil || req.WorkspaceTargetID != "native" || !bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatalf("req=%+v err=%v", req, err)
	}
}
