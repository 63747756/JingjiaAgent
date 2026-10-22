package runtimeadapter

import (
	"github.com/63747756/jingjiaagent/backend/config"
	"testing"
)

func TestPortableImageOnlyTranslatesVerifiedBundledDigest(t *testing.T) {
	n := config.RuntimeNode{GuestImage: "sha256:oci-index", InstalledGuestImage: "sha256:docker-config"}
	for _, req := range []string{"", n.GuestImage} {
		if runtimeImage(n, req) != n.InstalledGuestImage {
			t.Fatal("bundle identity not resolved")
		}
	}
	if runtimeImage(n, "custom:1") != "custom:1" {
		t.Fatal("custom image silently substituted")
	}
	n.InstalledGuestImage = ""
	if runtimeImage(n, "") != n.GuestImage {
		t.Fatal("legacy image changed")
	}
}
