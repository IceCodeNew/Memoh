package channel

import (
	"context"
	"maps"
	"strings"

	"github.com/felinics/memoh/internal/markdownmedia"
)

// Markdown media on a stream: prose streams as usual, and only text that may
// still become a media reference is withheld. Once a complete reference is
// withheld, everything after it waits for the final message, so the published
// files keep their place in the text. A reference written before a tool call
// carries its segment to the final, which binds it to the run's persisted
// snapshot instead of reading a workspace file that a later step may change.

func (s *managerOutboundStream) markdownMediaEnabled() bool {
	return s.send != nil && !isLocalChannel(s.channelType)
}

// pushMarkdown handles the stream events that publication depends on. It
// reports whether the event was fully handled; otherwise Push continues with
// the (possibly updated) event.
func (s *managerOutboundStream) pushMarkdown(ctx context.Context, event *StreamEvent) (bool, error) {
	if !s.markdownMediaEnabled() {
		return false, nil
	}
	switch event.Type {
	case StreamEventDelta:
		if event.Phase == StreamPhaseReasoning || event.Delta == "" {
			return false, nil
		}
		s.markdownFinalParts = nil
		s.markdownFinalNext = 0
		return true, s.pushTextDelta(ctx, *event)
	case StreamEventPhaseEnd:
		if event.Phase == StreamPhaseText && !s.holdingMedia {
			return false, s.releaseHeld(ctx)
		}
	case StreamEventSegmentEnd, StreamEventToolCallStart:
		return false, s.endTextSegment(ctx)
	case StreamEventReset:
		s.resetMarkdown()
	case StreamEventFinal:
		return s.pushMarkdownFinal(ctx, event)
	}
	return false, nil
}

func (s *managerOutboundStream) pushTextDelta(ctx context.Context, event StreamEvent) error {
	if s.holdingMedia {
		s.heldText.WriteString(event.Delta)
		return nil
	}
	pending := s.heldText.String() + event.Delta
	s.heldText.Reset()
	// Scan the whole segment so code fences opened by earlier deltas still
	// count as code.
	offset, media := markdownmedia.StreamHold(s.segmentText.String() + pending)
	offset = max(offset-s.segmentText.Len(), 0)
	if offset > 0 {
		release := event
		release.Delta = pending[:offset]
		if err := s.forwardText(ctx, release); err != nil {
			return err
		}
	}
	s.heldText.WriteString(pending[offset:])
	s.holdingMedia = media
	return nil
}

// forwardText streams text in pieces no longer than the platform limit, so a
// released buffer cannot exceed it as one delta after a split.
func (s *managerOutboundStream) forwardText(ctx context.Context, event StreamEvent) error {
	text := event.Delta
	step := s.policy.TextChunkLimit
	for text != "" {
		piece := text
		if step > 0 {
			piece = runePrefix(text, step)
		}
		chunk := event
		chunk.Delta = piece
		if err := s.pushDelta(ctx, chunk); err != nil {
			return err
		}
		s.segmentText.WriteString(piece)
		text = text[len(piece):]
	}
	return nil
}

// releaseHeld streams withheld text that did not become a reference.
func (s *managerOutboundStream) releaseHeld(ctx context.Context) error {
	if s.heldText.Len() == 0 {
		return nil
	}
	held := s.heldText.String()
	s.heldText.Reset()
	s.holdingMedia = false
	return s.forwardText(ctx, StreamEvent{Type: StreamEventDelta, Delta: held, Phase: StreamPhaseText})
}

// endTextSegment runs before the adapter commits the segment's text as its
// own message.
func (s *managerOutboundStream) endTextSegment(ctx context.Context) error {
	if s.holdingMedia {
		if s.heldText.Len() > 0 {
			s.carried = append(s.carried, s.heldText.String())
		}
		s.heldText.Reset()
	} else if err := s.releaseHeld(ctx); err != nil {
		return err
	}
	s.segmentText.Reset()
	return nil
}

func (s *managerOutboundStream) resetMarkdown() {
	s.segmentText.Reset()
	s.heldText.Reset()
	s.holdingMedia = false
	s.carried = nil
	s.markdownFinalSource = ""
	s.markdownFinalParts = nil
	s.markdownFinalNext = 0
}

func (s *managerOutboundStream) pushMarkdownFinal(ctx context.Context, event *StreamEvent) (bool, error) {
	if event.Final == nil {
		return false, nil
	}
	final := *event.Final
	if s.markdownFinalParts != nil && s.markdownFinalSource == final.Message.Text {
		// A retried final continues after the parts already acknowledged.
		return true, s.deliverMarkdownFinal(ctx)
	}
	var earlier []markdownmedia.Binding
	if value, ok := final.Message.Metadata[markdownmedia.EarlierMetadataKey]; ok {
		earlier = markdownmedia.Bindings(map[string]any{markdownmedia.MetadataKey: value})
		final.Message.Metadata = maps.Clone(final.Message.Metadata)
		delete(final.Message.Metadata, markdownmedia.EarlierMetadataKey)
	}
	final.Message = resolveMarkdownMessage(ctx, s.manager.attachmentStore, s.config, final.Message)
	if len(markdownmedia.Bindings(final.Message.Metadata)) == 0 && len(s.carried) == 0 {
		// Nothing to publish: the withheld text belongs to the reply body.
		if err := s.releaseHeld(ctx); err != nil {
			return true, err
		}
		event.Final = &final
		return false, nil
	}
	parts, err := s.prepareMarkdownFinal(final.Message, earlier)
	if err != nil {
		return true, err
	}
	s.markdownFinalSource = final.Message.Text
	s.markdownFinalParts = parts
	s.markdownFinalNext = 0
	return true, s.deliverMarkdownFinal(ctx)
}

// prepareMarkdownFinal orders everything the final publishes: carried
// segments from earlier steps, then the final message. Text the adapter has
// already streamed is not repeated.
func (s *managerOutboundStream) prepareMarkdownFinal(msg Message, earlier []markdownmedia.Binding) ([]OutboundMessage, error) {
	caps, hasCaps := s.manager.registry.GetOutboundCapabilities(s.channelType, s.config, s.target)
	finalParts := []OutboundMessage(nil)
	if !msg.IsEmpty() {
		expanded, ok := expandMarkdownMessage(OutboundMessage{Target: s.target, Message: msg})
		if !ok {
			expanded = []OutboundMessage{{Target: s.target, Message: msg}}
		}
		finalParts = expanded
	}
	if len(s.carried) == 0 && s.splitCount == 0 {
		// The adapter's message becomes the leading prose, so nothing that
		// was streamed is lost or repeated.
		return buildMarkdownParts(finalParts, s.policy, caps, hasCaps)
	}

	// Commit what the adapter has streamed, then send the rest separately.
	commit := Message{Reply: msg.Reply, Thread: msg.Thread, Metadata: maps.Clone(msg.Metadata)}
	delete(commit.Metadata, markdownmedia.MetadataKey)
	parts := []OutboundMessage{{Target: s.target, Message: commit}}
	for _, text := range s.carried {
		carried := Message{Format: MessageFormatMarkdown, Text: text, Reply: msg.Reply, Thread: msg.Thread, Metadata: maps.Clone(msg.Metadata)}
		if carried.Metadata == nil {
			carried.Metadata = map[string]any{}
		}
		carried.Metadata[markdownmedia.MetadataKey] = markdownmedia.Rebind(text, &earlier)
		expanded, ok := expandMarkdownMessage(OutboundMessage{Target: s.target, Message: carried})
		if !ok {
			expanded = []OutboundMessage{{Target: s.target, Message: carried}}
		}
		built, err := buildMarkdownParts(expanded, s.policy, caps, hasCaps)
		if err != nil {
			return nil, err
		}
		parts = append(parts, built...)
	}
	if len(s.carried) == 0 && len(finalParts) > 0 && len(finalParts[0].Message.Attachments) == 0 {
		finalParts[0].Message.Text = trimStreamedProse(finalParts[0].Message.Text, s.segmentText.String())
	}
	built, err := buildMarkdownParts(finalParts, s.policy, caps, hasCaps)
	if err != nil {
		return nil, err
	}
	return append(parts, built...), nil
}

func buildMarkdownParts(parts []OutboundMessage, policy OutboundPolicy, caps ChannelCapabilities, hasCaps bool) ([]OutboundMessage, error) {
	nonEmpty := parts[:0:0]
	for _, part := range parts {
		if !part.Message.IsEmpty() {
			nonEmpty = append(nonEmpty, part)
		}
	}
	if len(nonEmpty) == 0 {
		return nil, nil
	}
	return buildExpandedMarkdownMessages(nonEmpty, policy, caps, hasCaps)
}

// trimStreamedProse removes the prefix the adapter already delivered.
func trimStreamedProse(prose, streamed string) string {
	if streamed == "" {
		return prose
	}
	if rest, ok := strings.CutPrefix(prose, streamed); ok {
		return rest
	}
	trimmedStreamed := strings.TrimSpace(streamed)
	trimmedProse := strings.TrimLeft(prose, " \t\r\n")
	if rest, ok := strings.CutPrefix(trimmedProse, trimmedStreamed); ok {
		return rest
	}
	if strings.HasPrefix(trimmedStreamed, strings.TrimSpace(prose)) {
		return ""
	}
	return prose
}

// deliverMarkdownFinal keeps progress across a retried final event, so parts
// that were already acknowledged are not republished. Platform transport
// retries retain their existing semantics.
func (s *managerOutboundStream) deliverMarkdownFinal(ctx context.Context) error {
	for s.markdownFinalNext < len(s.markdownFinalParts) {
		part := s.markdownFinalParts[s.markdownFinalNext]
		var err error
		// Some adapters finalize text only (for example Discord). A leading
		// attachment must use the ordinary sender, not a text final event.
		if s.markdownFinalNext == 0 && len(part.Message.Attachments) == 0 {
			first := StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: part.Message}}
			if s.splitCount > 0 {
				err = s.pushFinalAfterSplit(ctx, first, part.Message.PlainText())
			} else {
				err = s.pushPrepared(ctx, first)
			}
		} else {
			err = s.send(ctx, part)
		}
		if err != nil {
			return err
		}
		s.markdownFinalNext++
	}
	s.carried = nil
	s.heldText.Reset()
	s.holdingMedia = false
	return nil
}

func runePrefix(text string, n int) string {
	count := 0
	for i := range text {
		if count == n {
			return text[:i]
		}
		count++
	}
	return text
}

// isLocalChannel reports surfaces that render Markdown from history: Web and
// Desktop open linked workspace files in place, and the CLI shows the text.
func isLocalChannel(channelType ChannelType) bool {
	switch strings.ToLower(strings.TrimSpace(string(channelType))) {
	case "web", "cli", string(ChannelTypeLocal):
		return true
	}
	return false
}
