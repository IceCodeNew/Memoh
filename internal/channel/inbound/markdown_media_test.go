package inbound

import (
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/markdownmedia"
	"github.com/felinics/memoh/internal/media"
)

func TestBindPublishedMediaUsesSnapshotAndPreservesRoutingMetadata(t *testing.T) {
	source := "[report](/data/report.txt)"
	binding := markdownmedia.Binding{Reference: markdownmedia.Parse(source)[0], Asset: media.Asset{ContentHash: "original-snapshot"}}
	published := []messagepkg.Message{{Metadata: map[string]any{"markdown_media_source": source, markdownmedia.MetadataKey: []markdownmedia.Binding{binding}}}}
	msg := bindPublishedMedia(channel.Message{Text: source, Metadata: map[string]any{"existing": "keep"}}, &published, "zh")
	refs := markdownmedia.Bindings(msg.Metadata)
	if len(refs) != 1 || refs[0].Asset.ContentHash != "original-snapshot" || msg.Metadata["existing"] != "keep" || len(published) != 0 {
		t.Fatalf("msg=%+v", msg)
	}
	// The already consumed publication cannot silently reread a changed file.
	msg = bindPublishedMedia(channel.Message{Text: source}, &published, "zh")
	refs = markdownmedia.Bindings(msg.Metadata)
	if len(refs) != 1 || refs[0].ErrorCode != "media.reference_unavailable" {
		t.Fatalf("refs=%+v", refs)
	}
}

func TestEarlierStepMediaIsCarriedToTheFinal(t *testing.T) {
	messages := []turn.ModelMessage{
		{Role: "assistant", Content: json.RawMessage(`"Created [report](/data/r.txt), now checking."`), ToolCalls: []turn.ToolCall{{Type: "function", Function: turn.ToolCallFunction{Name: "exec", Arguments: `{}`}}}},
		{Role: "tool", Content: json.RawMessage(`"ok"`)},
		{Role: "assistant", Content: json.RawMessage(`"All tests pass."`)},
	}
	if !runPublishesMarkdownMedia(messages) {
		t.Fatal("text written before a tool call was not considered")
	}
	if runPublishesMarkdownMedia(messages[1:]) {
		t.Fatal("run without references loads snapshots")
	}
	ref := markdownmedia.Parse("[report](/data/r.txt)")[0]
	step := messagepkg.Message{Metadata: map[string]any{
		"markdown_media_source":   "Created [report](/data/r.txt), now checking.",
		markdownmedia.MetadataKey: []markdownmedia.Binding{{Reference: ref, Asset: media.Asset{ContentHash: "step-one"}}},
	}}
	published := []messagepkg.Message{step}
	final := bindPublishedMedia(channel.Message{Text: "All tests pass."}, &published, "en")
	final = withEarlierMedia(final, earlierPublishedMedia(published), "en")
	earlier := markdownmedia.Bindings(map[string]any{markdownmedia.MetadataKey: final.Metadata[markdownmedia.EarlierMetadataKey]})
	if len(earlier) != 1 || earlier[0].Asset.ContentHash != "step-one" || len(markdownmedia.Bindings(final.Metadata)) != 0 {
		t.Fatalf("final=%+v", final)
	}
}
