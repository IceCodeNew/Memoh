package workspace

import (
	"errors"
	"slices"
	"testing"

	dbstore "github.com/felinics/memoh/internal/db/store"
)

func TestCapabilitiesForManagedOffersEverything(t *testing.T) {
	t.Parallel()

	got := CapabilitiesFor(WorkspaceKindManaged)
	for _, capability := range allCapabilities {
		if !got.Has(capability) {
			t.Fatalf("managed computer lacks %s: %v", capability, got.List())
		}
	}
	if !slices.Equal(got.List(), allCapabilities) {
		t.Fatalf("List() = %v, want %v", got.List(), allCapabilities)
	}
	if err := got.Require(CapabilityExternalAgent); err != nil {
		t.Fatalf("Require(external_agent) on managed = %v", err)
	}
}

func TestCapabilitiesForUserOffersFilesOnly(t *testing.T) {
	t.Parallel()

	got := CapabilitiesFor(WorkspaceKindUser)
	if !got.Has(CapabilityFiles) {
		t.Fatalf("user computer lacks files: %v", got.List())
	}
	for _, capability := range []Capability{CapabilityTerminal, CapabilityDisplay, CapabilitySnapshots, CapabilityDependencies, CapabilityExternalAgent} {
		if got.Has(capability) {
			t.Fatalf("user computer offers %s", capability)
		}
		err := got.Require(capability)
		if !errors.Is(err, ErrCapabilityUnsupported) {
			t.Fatalf("Require(%s) = %v, want ErrCapabilityUnsupported", capability, err)
		}
		var capErr *CapabilityError
		if !errors.As(err, &capErr) || capErr.Capability != capability {
			t.Fatalf("Require(%s) error = %#v, want CapabilityError naming it", capability, err)
		}
	}
	if !slices.Equal(got.List(), []Capability{CapabilityFiles}) {
		t.Fatalf("List() = %v, want [files]", got.List())
	}
}

func TestCapabilitiesForUnknownKindOffersNothing(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", "  ", "vm", "MANAGED"} {
		got := CapabilitiesFor(kind)
		if len(got.List()) != 0 {
			t.Fatalf("CapabilitiesFor(%q) = %v, want nothing", kind, got.List())
		}
	}
	var none Capabilities
	if none.Has(CapabilityFiles) {
		t.Fatal("nil set offers files")
	}
	if err := none.Require(CapabilityFiles); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("nil set Require = %v, want ErrCapabilityUnsupported", err)
	}
}

func TestRecordCapabilitiesFollowsKind(t *testing.T) {
	t.Parallel()

	managed := RecordCapabilities(dbstore.WorkspaceRecord{ID: wsTestManagedID, Kind: WorkspaceKindManaged})
	user := RecordCapabilities(dbstore.WorkspaceRecord{ID: wsTestRuntimeID, Kind: WorkspaceKindUser})
	if !managed.Has(CapabilityTerminal) || user.Has(CapabilityTerminal) {
		t.Fatalf("managed terminal = %v, user terminal = %v", managed.Has(CapabilityTerminal), user.Has(CapabilityTerminal))
	}
}

func TestIsUserComputerTargetIsAnOwnershipQuestion(t *testing.T) {
	t.Parallel()

	if !IsUserComputerTarget(WorkspaceTargetRemote) || !IsUserComputerTarget(" remote ") {
		t.Fatal("remote target kind is a user's computer")
	}
	if IsUserComputerTarget(WorkspaceTargetNative) || IsUserComputerTarget("") {
		t.Fatal("native target kind is the server's computer")
	}
}
