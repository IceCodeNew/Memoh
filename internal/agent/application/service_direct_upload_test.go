package application

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDirectChatCancellationDuringModelRequestUpload(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelStallHeaders)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := fixture.service.nonStreamingHTTPClient
	client.Transport = cancelUploadTransport{base: client.Transport, cancel: cancel}

	_, err := fixture.service.Chat(ctx, ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                strings.Repeat("x", 32*1024),
		UserMessagePersisted: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want caller cancellation during upload", err)
	}
}

type cancelUploadTransport struct {
	base   http.RoundTripper
	cancel context.CancelFunc
}

func (t cancelUploadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Body = &cancelUploadBody{ReadCloser: req.Body, cancel: t.cancel}
	return t.base.RoundTrip(req)
}

type cancelUploadBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	read   bool
}

func (b *cancelUploadBody) Read(p []byte) (int, error) {
	if b.read {
		b.cancel()
		return 0, context.Canceled
	}
	b.read = true
	return b.ReadCloser.Read(p[:min(len(p), 8192)])
}
