package hooks

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type countingHookBridgeServer struct {
	*hookBridgeTestServer
	reads atomic.Int32
}

func (s *countingHookBridgeServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.reads.Add(1)
	return s.hookBridgeTestServer.ReadRaw(req, stream)
}

func newCountingHookService(t *testing.T, config string) (*Service, *countingHookBridgeServer) {
	t.Helper()
	files := map[string][]byte{}
	if config != "" {
		files[DefaultConfigPath] = []byte(config)
	}
	server := &countingHookBridgeServer{hookBridgeTestServer: &hookBridgeTestServer{files: files}}
	return NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)}), server
}

func TestTurnSnapshotLoadsConfigOncePerTurn(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version":1,"hooks":[{"event":"UserMessageReceived","actions":[{"type":"command","command":"true"}]}]}`)
	ctx := WithTurnSnapshot(t.Context())
	if WithTurnSnapshot(ctx) != ctx {
		t.Fatal("WithTurnSnapshot replaced the turn's snapshot")
	}
	for range 3 {
		cfg, exists, err := service.LoadEffective(ctx, "bot-1")
		if err != nil || !exists || len(cfg.Hooks) != 1 {
			t.Fatalf("cfg=%+v exists=%v err=%v", cfg, exists, err)
		}
	}
	if got := server.reads.Load(); got != 1 {
		t.Fatalf("config read %d times in one turn, want 1", got)
	}

	if _, _, err := service.LoadEffective(t.Context(), "bot-1"); err != nil {
		t.Fatal(err)
	}
	if got := server.reads.Load(); got != 2 {
		t.Fatalf("a load outside the turn reused its snapshot: reads = %d", got)
	}
}

func TestBrokenConfigIsUnavailableForTheWholeTurn(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version": 1, "hooks": [`)
	ctx := WithTurnSnapshot(t.Context())
	for range 2 {
		if _, _, err := service.LoadEffective(ctx, "bot-1"); !errors.Is(err, ErrConfigUnavailable) {
			t.Fatalf("LoadEffective error = %v, want ErrConfigUnavailable", err)
		}
	}
	if _, err := service.Run(ctx, Request{Event: EventPreToolUse, BotID: "bot-1"}, nil); !errors.Is(err, ErrConfigUnavailable) {
		t.Fatalf("Run error = %v, want ErrConfigUnavailable so policy gates fail closed", err)
	}
	if got := server.reads.Load(); got != 1 {
		t.Fatalf("broken config read %d times in one turn, want 1", got)
	}
}

func TestUnavailableWorkspaceSkipsConfigRead(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version":1,"hooks":[]}`)
	ctx := bridge.WithWorkspaceAvailability(t.Context())
	bridge.MarkWorkspaceUnavailable(ctx)
	if _, _, err := service.LoadEffective(ctx, "bot-1"); !errors.Is(err, ErrConfigUnavailable) || !errors.Is(err, bridge.ErrUnavailable) {
		t.Fatalf("LoadEffective error = %v, want ErrConfigUnavailable caused by the unavailable workspace", err)
	}
	if got := server.reads.Load(); got != 0 {
		t.Fatalf("config read %d times from an unavailable workspace", got)
	}
}

func TestMissingConfigIsNoHooks(t *testing.T) {
	t.Parallel()

	service, _ := newCountingHookService(t, "")
	cfg, exists, err := service.LoadEffective(WithTurnSnapshot(t.Context()), "bot-1")
	if err != nil || exists || len(cfg.Hooks) != 0 {
		t.Fatalf("cfg=%+v exists=%v err=%v, want no hooks", cfg, exists, err)
	}
}
