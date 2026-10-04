package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/domain"
	"github.com/chaitin/MonkeyCode/backend/errcode"
)

func TestComposeTeamInstallerCannotGenerateTaskflowCommand(t *testing.T) {
	u := &TeamHostUsecase{cfg: &config.Config{Runtime: config.Runtime{Backend: "agent_compose"}}}
	if _, err := u.GetInstallCommand(context.Background(), &domain.TeamUser{}); !errors.Is(err, errcode.ErrRuntimeNodeInstaller) {
		t.Fatal("compose generated legacy team install command")
	}
}
