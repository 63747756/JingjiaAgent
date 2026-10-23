package v1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
)

func TestSetupDeploymentOrigin(t *testing.T) {
	token := strings.Repeat("a", 64)
	tokenFile := filepath.Join(t.TempDir(), "setup.token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, configured, origin string
		allowed                  bool
	}{
		{"http default port", "http://192.168.1.20:80", "http://192.168.1.20", true},
		{"https default port and case", "https://Agent.Internal:443/", "https://agent.internal", true},
		{"custom port and case", "http://Agent.Internal:8080", "http://agent.internal:8080", true},
		{"ipv6 canonical form", "http://[FD00:0:0:0:0:0:0:12]:80", "http://[fd00::12]", true},
		{"same origin", "http://agent.internal", "http://agent.internal", true},
		{"no origin still requires token", "http://agent.internal", "", true},
		{"different port", "http://agent.internal:8080", "http://agent.internal", false},
		{"different scheme", "http://agent.internal", "https://agent.internal", false},
		{"different host", "http://agent.internal", "http://other.internal", false},
		{"suffix host", "http://agent.internal", "http://agent.internal.evil", false},
		{"null origin", "http://agent.internal", "null", false},
		{"credentials", "http://agent.internal", "http://user@agent.internal", false},
		{"path", "http://agent.internal", "http://agent.internal/path", false},
		{"query", "http://agent.internal", "http://agent.internal?", false},
		{"fragment", "http://agent.internal", "http://agent.internal#", false},
		{"empty port", "http://agent.internal", "http://agent.internal:", false},
		{"invalid port", "http://agent.internal", "http://agent.internal:65536", false},
		{"multiple origins", "http://agent.internal", "http://agent.internal http://other.internal", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Server.BaseURL = tc.configured
			cfg.Deployment.Enabled = true
			cfg.Deployment.SetupTokenFile = tokenFile
			repo := &deploymentInitializerStub{}
			h := &TeamGroupUserHandler{config: cfg, repo: repo}
			ctx := teamTestWebContext()
			ctx.Request().Header.Set("Origin", tc.origin)
			req := deploymentSetup{Token: token, Email: "admin@example.com", Name: "Admin", Password: "test-password-123"}
			err := h.SetupDeployment(ctx, req)
			if tc.allowed {
				if err != nil || !repo.called {
					t.Fatalf("equivalent origin rejected: called=%v error=%v", repo.called, err)
				}
				// Equivalent origins must not bypass the initialization credential.
				repo.called = false
				req.Token = strings.Repeat("b", 64)
				if err = h.SetupDeployment(ctx, req); !errors.Is(err, errcode.ErrForbidden) || repo.called {
					t.Fatal("invalid setup token accepted", err)
				}
			} else if !errors.Is(err, errcode.ErrForbidden) || repo.called {
				t.Fatalf("cross-origin request reached initialization: called=%v error=%v", repo.called, err)
			}
		})
	}
}

type deploymentInitializerStub struct {
	domain.TeamGroupUserRepo
	called bool
}

func (s *deploymentInitializerStub) InitDeployment(context.Context, string, string, string, string) (*domain.InitTeamResult, error) {
	s.called = true
	return &domain.InitTeamResult{}, nil
}
