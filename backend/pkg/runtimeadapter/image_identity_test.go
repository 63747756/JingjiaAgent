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

func TestRecoveredImagesRemainNodeLocalAndDoNotReplaceCustomTags(t *testing.T) {
	n := config.RuntimeNode{GuestImage: "old-default", InstalledGuestImage: "old-installed", RecoveredImages: map[string]string{"old-installed": "restored-default", "sha256:old": "sha256:same-content", "registry/image@sha256:old": "sha256:same-content"}}
	for request, want := range map[string]string{"": "restored-default", "old-default": "restored-default", "sha256:old": "sha256:same-content", "registry/image@sha256:old": "sha256:same-content", "custom:latest": "custom:latest"} {
		if got := runtimeImage(n, request); got != want {
			t.Fatalf("%s: %s != %s", request, got, want)
		}
	}
	if got := runtimeImage(config.RuntimeNode{}, "sha256:old"); got != "sha256:old" {
		t.Fatal("remote node changed")
	}
}
