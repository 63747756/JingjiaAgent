package taskflow

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsVirtualMachineNotFoundRecognizesOnlyConfirmedMissingRuntime(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"runtime exact", errors.New("virtual_machine not found"), true},
		{"runtime wrapped", fmt.Errorf("remove environment: %w", errors.New("virtual_machine not found")), true},
		{"legacy", errors.New("environment not found: vm-1"), true},
		{"permission with misleading text", errors.New("permission denied: virtual_machine not found"), false},
		{"unknown error", errors.New("runtime unavailable"), false},
		{"partial text", errors.New("virtual_machine not found while checking permission"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsVirtualMachineNotFound(tc.err); got != tc.want {
				t.Fatalf("missing=%v want %v", got, tc.want)
			}
		})
	}
}
