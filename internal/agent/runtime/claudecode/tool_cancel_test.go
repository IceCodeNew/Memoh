package claudecode

import (
	"context"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

// toolCallingCLI calls the Memoh tool gateway once per prompt. On interrupt
// it reports the result and exits without waiting for that call, the way the
// real CLI leaves an in-flight Memoh tool behind; "finish" in the working
// directory makes it end the turn normally while the call is still open.
const toolCallingCLI = `#!/usr/bin/env python3
import json, os, sys, threading, time, urllib.request
args = sys.argv[1:]
url = json.loads(args[args.index("--mcp-config") + 1])["mcpServers"]["memoh"]["url"]
def send(value):
    sys.stdout.write(json.dumps(value) + "\n")
    sys.stdout.flush()
def call():
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "hold", "arguments": {}}}).encode()
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Mcp-Protocol-Version": "2025-11-25"}
    try:
        urllib.request.urlopen(urllib.request.Request(url, body, headers), timeout=60).read()
    except Exception:
        pass
def finish_when_released():
    while not os.path.exists("finish"):
        time.sleep(0.02)
    send({"type": "result", "subtype": "success", "session_id": "native", "is_error": False, "result": "done"})
    os._exit(0)
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("type") == "control_request":
        send({"type": "control_response", "response": {"subtype": "success", "request_id": msg["request_id"], "response": {}}})
        if msg["request"]["subtype"] == "interrupt":
            send({"type": "result", "subtype": "error_during_execution", "session_id": "native", "is_error": True})
            os._exit(0)
    elif msg.get("type") == "user":
        send({"type": "system", "subtype": "init", "session_id": "native", "model": "fixture"})
        threading.Thread(target=call, daemon=True).start()
        threading.Thread(target=finish_when_released, daemon=True).start()
`

type heldTool struct {
	started chan string
	stopped chan string
}

func (*heldTool) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return []mcp.ToolDescriptor{{Name: "hold", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (h *heldTool) CallTool(ctx context.Context, session mcp.ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	h.started <- session.SessionID
	select {
	case <-ctx.Done():
		h.stopped <- session.SessionID
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return mcp.BuildToolSuccessResult(map[string]any{"ok": true}), nil
	}
}

type toolTurnBridge struct {
	client *bridge.Client
	info   bridge.WorkspaceInfo
}

func (b toolTurnBridge) MCPClient(context.Context, string) (*bridge.Client, error) {
	return b.client, nil
}

func (b toolTurnBridge) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return b.info, nil
}

type toolTurnAgents struct{ dbstore.Queries }

func (toolTurnAgents) GetBotAgentByID(context.Context, sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	return sqlc.BotAgent{Runtime: RuntimeType, Enabled: true, Metadata: []byte(`{"auth":"workspace"}`), CreatedAt: pgtype.Timestamptz{Valid: true}, UpdatedAt: pgtype.Timestamptz{Valid: true}}, nil
}

type toolTurnApprovals struct{ approval.FlowService }

func (toolTurnApprovals) RegisterWaiter(string) func() { return func() {} }

type toolTurnLauncher string

func (l toolTurnLauncher) ResolveLauncher(context.Context, string, string) (external.Launcher, error) {
	return external.Launcher{Path: string(l)}, nil
}

type toolTurnHarness struct {
	driver *Driver
	tool   *heldTool
	root   string
}

func newToolTurnHarness(t *testing.T) *toolTurnHarness {
	t.Helper()
	root := t.TempDir()
	broker := bridgesvc.NewReverseHTTPBroker()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: root, WorkspaceRoot: root, DataMount: "/data", AllowHostAbsolute: true, ReverseHTTP: broker}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///claude-tool-cancel", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	proxy := httptest.NewServer(broker)
	t.Cleanup(proxy.Close)
	launcher := filepath.Join(root, "claude")
	if err := os.WriteFile(launcher, []byte(toolCallingCLI), 0o700); err != nil { //nolint:gosec // test launcher must be executable.
		t.Fatal(err)
	}
	tool := &heldTool{started: make(chan string, 4), stopped: make(chan string, 4)}
	logger := slog.New(slog.DiscardHandler)
	return &toolTurnHarness{
		driver: &Driver{
			bridges:     toolTurnBridge{bridge.NewClientFromConn(conn), bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, ToolsHTTPURL: proxy.URL + "/mcp"}},
			agents:      botagents.NewService(logger, toolTurnAgents{}),
			approval:    toolTurnApprovals{},
			toolGateway: toolmount.Gateway{Tools: mcp.NewToolGatewayService(logger, []mcp.ToolSource{tool}), Contexts: mcp.NewToolSessionContextStore(), Logger: logger},
			logger:      logger,
			launchers:   toolTurnLauncher(launcher),
		},
		tool: tool,
		root: root,
	}
}

type toolTurnResult struct {
	result external.PromptResult
	err    error
}

func (h *toolTurnHarness) prompt(ctx context.Context, t *testing.T, threadID string) <-chan toolTurnResult {
	t.Helper()
	workDir := filepath.Join(h.root, threadID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan toolTurnResult, 1)
	go func() {
		result, err := h.driver.Prompt(ctx, external.PromptInput{
			BotID: "11111111-1111-4111-8111-111111111111", BotAgentID: "22222222-2222-4222-8222-222222222222",
			ThreadID: threadID, RunID: threadID + "-run", SessionMode: "subagent", Prompt: "hold",
			RuntimeMetadata: map[string]any{"project_path": workDir}, Sink: &recordingSink{},
		})
		done <- toolTurnResult{result, err}
	}()
	return done
}

func (h *toolTurnHarness) awaitStarted(t *testing.T, want ...string) {
	t.Helper()
	pending := map[string]bool{}
	for _, id := range want {
		pending[id] = true
	}
	for len(pending) > 0 {
		select {
		case id := <-h.tool.started:
			delete(pending, id)
		case <-time.After(10 * time.Second):
			t.Fatalf("Memoh tool calls never started: %v", pending)
		}
	}
}

func awaitTurn(t *testing.T, done <-chan toolTurnResult, why string) toolTurnResult {
	t.Helper()
	select {
	case completed := <-done:
		return completed
	case <-time.After(5 * time.Second):
		t.Fatal(why)
		return toolTurnResult{}
	}
}

func TestStopCancelsOnlyTheStoppedRunsMemohTool(t *testing.T) {
	h := newToolTurnHarness(t)
	ctxA, stopA := context.WithCancel(t.Context())
	defer stopA()
	ctxB, stopB := context.WithCancel(t.Context())
	defer stopB()
	doneA := h.prompt(ctxA, t, "child-a")
	doneB := h.prompt(ctxB, t, "child-b")
	h.awaitStarted(t, "child-a", "child-b")

	stopA()
	select {
	case id := <-h.tool.stopped:
		if id != "child-a" {
			t.Fatalf("stopping child-a cancelled %s's tool", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel the run's in-flight Memoh tool")
	}
	if completed := awaitTurn(t, doneA, "stopped turn waited for its Memoh tool"); completed.err != nil || completed.result.TerminationUnconfirmed {
		t.Fatalf("stopped turn = %+v, %v", completed.result, completed.err)
	}
	select {
	case id := <-h.tool.stopped:
		t.Fatalf("%s's tool stopped with the other run", id)
	case completed := <-doneB:
		t.Fatalf("the other run ended with the stopped one: %+v, %v", completed.result, completed.err)
	case <-time.After(300 * time.Millisecond):
	}

	stopB()
	if id := <-h.tool.stopped; id != "child-b" {
		t.Fatalf("stopped tool = %s", id)
	}
	awaitTurn(t, doneB, "second stopped turn waited for its Memoh tool")
}

func TestTurnEndCancelsAMemohToolTheCLILeftOpen(t *testing.T) {
	h := newToolTurnHarness(t)
	done := h.prompt(t.Context(), t, "child")
	h.awaitStarted(t, "child")
	if err := os.WriteFile(filepath.Join(h.root, "child", "finish"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	completed := awaitTurn(t, done, "finished turn waited for the Memoh tool its CLI left open")
	if completed.err != nil || !completed.result.TurnCompleted || completed.result.Text != "done" {
		t.Fatalf("finished turn = %+v, %v", completed.result, completed.err)
	}
	select {
	case <-h.tool.stopped:
	default:
		t.Fatal("turn ended with its Memoh tool still running")
	}
}
