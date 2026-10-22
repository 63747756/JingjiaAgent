package config

import "testing"

func TestRuntimeCallbackConfiguration(t *testing.T) {
	for _, test := range []struct {
		url   string
		valid bool
	}{
		{"", true},
		{" http://127.0.0.1:8888 ", true},
		{"http://[::1]:8888/", true},
		{"https://backend.internal", true},
		{"http://192.168.242.10:47424", false},
		{"http://backend:8888", false},
		{"https://user:secret@backend.internal", false},
		{"https://backend.internal/internal/vm-ready", false},
		{"https://backend.internal?token=secret", false},
		{"https://backend.internal#secret", false},
		{"file:///tmp/callback", false},
	} {
		runtime := Runtime{CallbackBaseURL: test.url}
		if valid := runtime.Validate() == nil; valid != test.valid {
			t.Errorf("callback URL %q accepted=%v, want %v", test.url, valid, test.valid)
		}
	}
	t.Setenv("JINGJIAAGENT_RUNTIME_BACKEND", "taskflow")
	t.Setenv("JINGJIAAGENT_RUNTIME_CALLBACK_BASE_URL", " http://127.0.0.1:8888 ")
	cfg, err := Init(t.TempDir())
	if err != nil || cfg.Runtime.CallbackBaseURL != "http://127.0.0.1:8888" {
		t.Fatal("internal lifecycle callback URL was not loaded and normalized")
	}
}
