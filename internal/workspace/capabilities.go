package workspace

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Capability names one thing a computer can do for a bot. Callers that need
// a terminal, a desktop, a snapshot or an external agent ask for the
// capability; they do not ask whether the computer is managed or a user's own.
type Capability string

const (
	// CapabilityFiles: file reads, writes and command execution over the bridge.
	CapabilityFiles Capability = "files"
	// CapabilityTerminal: an interactive PTY.
	CapabilityTerminal Capability = "terminal"
	// CapabilityDisplay: the GUI desktop and the headed browser (Browser Use,
	// Computer Use, the display stream).
	CapabilityDisplay Capability = "display"
	// CapabilitySnapshots: snapshots, versions and resource limits of the
	// container the server owns.
	CapabilitySnapshots Capability = "snapshots"
	// CapabilityDependencies: managed dependency installation and the stdio
	// MCP servers it hosts.
	CapabilityDependencies Capability = "dependencies"
	// CapabilityExternalAgent: Codex, Claude Code and ACP agents run on the
	// computer and bind its working directories.
	CapabilityExternalAgent Capability = "external_agent"
)

// allCapabilities is every capability a managed computer offers, in a
// stable order for messages and tests.
var allCapabilities = []Capability{
	CapabilityFiles,
	CapabilityTerminal,
	CapabilityDisplay,
	CapabilitySnapshots,
	CapabilityDependencies,
	CapabilityExternalAgent,
}

// Capabilities is the set a computer offers. A nil set offers nothing.
type Capabilities map[Capability]bool

// CapabilitiesFor derives the set from a workspace kind: a managed computer is
// a server-owned container and offers everything; a user's own computer is
// reached through its Remote Runtime, which only exposes files and commands.
// An unknown kind offers nothing.
func CapabilitiesFor(kind string) Capabilities {
	switch strings.TrimSpace(kind) {
	case WorkspaceKindManaged:
		set := make(Capabilities, len(allCapabilities))
		for _, capability := range allCapabilities {
			set[capability] = true
		}
		return set
	case WorkspaceKindUser:
		return Capabilities{CapabilityFiles: true}
	}
	return Capabilities{}
}

// RecordCapabilities is CapabilitiesFor on a stored workspace.
func RecordCapabilities(record dbstore.WorkspaceRecord) Capabilities {
	return CapabilitiesFor(record.Kind)
}

// Has reports whether the computer offers the capability.
func (c Capabilities) Has(capability Capability) bool {
	return c[capability]
}

// Require is Has as an error: ErrCapabilityUnsupported, carrying the
// capability, when the computer lacks it.
func (c Capabilities) Require(capability Capability) error {
	if c.Has(capability) {
		return nil
	}
	return &CapabilityError{Capability: capability}
}

// List returns the offered capabilities in declaration order.
func (c Capabilities) List() []Capability {
	out := make([]Capability, 0, len(c))
	for _, capability := range allCapabilities {
		if c[capability] {
			out = append(out, capability)
		}
	}
	for capability, offered := range c {
		if offered && !slices.Contains(allCapabilities, capability) {
			out = append(out, capability)
		}
	}
	return out
}

// ErrCapabilityUnsupported: the computer cannot serve the operation because
// its kind does not carry the capability it needs.
var ErrCapabilityUnsupported = errors.New("workspace does not support this operation")

// CapabilityError is ErrCapabilityUnsupported together with the capability
// that was required.
type CapabilityError struct {
	Capability Capability
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("%s: %s is not available on this computer", ErrCapabilityUnsupported.Error(), e.Capability)
}

func (*CapabilityError) Is(target error) bool {
	return target == ErrCapabilityUnsupported
}
