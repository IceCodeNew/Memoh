package workspace

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNativeCapabilityUnavailableIncludesTimeoutsOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"provider timeout":  {fmt.Errorf("workspace info: %w", status.Error(codes.DeadlineExceeded, "sandbox provider timed out")), true},
		"caller deadline":   {fmt.Errorf("mcp client: %w", context.DeadlineExceeded), true},
		"container missing": {ErrContainerNotFound, true},
		"other failure":     {errors.New("permission denied"), false},
	} {
		if got := nativeCapabilityUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}
