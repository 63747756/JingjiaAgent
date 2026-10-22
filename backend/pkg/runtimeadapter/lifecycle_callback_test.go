package runtimeadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
)

func TestLifecycleCallbackUsesInternalURL(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/internal/vm-ready" || r.Header.Get("Authorization") != "Bearer callback-test-token" {
			t.Errorf("lifecycle callback lost its path or authentication")
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["id"] != "existing-environment" {
			t.Errorf("lifecycle callback lost the environment identity")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Server.BaseURL = "http://192.0.2.1:47424"
	cfg.Runtime.CallbackBaseURL = server.URL
	client := &Client{callbackURL: runtimeCallbackURL(cfg), callbackToken: "callback-test-token", http: server.Client()}
	if err := client.callback(context.Background(), "/internal/vm-ready", map[string]string{"id": "existing-environment"}); err != nil {
		t.Fatal(err)
	}
	if !called || cfg.Server.BaseURL != "http://192.0.2.1:47424" {
		t.Fatal("the internal callback must preserve the public platform URL")
	}
	cfg.Runtime.CallbackBaseURL = ""
	if runtimeCallbackURL(cfg) != cfg.Server.BaseURL {
		t.Fatal("an unset internal callback URL must retain the existing configuration")
	}
}
