package v1

import (
	"errors"
	"fmt"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func TestFileAccessDenialPreservesMaskedNotFound(t *testing.T) {
	if err := wraperr(fmt.Errorf("VM lookup: %w", errcode.ErrNotFound), "/workspace/private.txt"); !errors.Is(err, errcode.ErrNotFound) {
		t.Fatal("authorization denial was replaced by a generic file operation failure")
	}
}

func TestValidateUploadFileSize(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{name: "empty file", size: 0},
		{name: "exact limit", size: maxUploadFileSize},
		{name: "over limit", size: maxUploadFileSize + 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUploadFileSize(tc.size)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateUploadFileSize(%d) error = %v, wantErr = %v", tc.size, err, tc.wantErr)
			}
		})
	}
}
