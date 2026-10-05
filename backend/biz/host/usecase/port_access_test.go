package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type portDeniedRepo struct {
	domain.HostRepo
	calls int
}

func (r *portDeniedRepo) GetVirtualMachineWithUser(context.Context, uuid.UUID, string) (*db.VirtualMachine, error) {
	r.calls++
	return nil, errcode.ErrNotFound
}

type portUnreachableClient struct {
	taskflow.Clienter
	called bool
}

func (c *portUnreachableClient) PortForwarder() taskflow.PortForwarder { c.called = true; return nil }

func TestPortMutationsCheckOriginalEnvironmentPermissionBeforeRuntime(t *testing.T) {
	repo := &portDeniedRepo{}
	runtime := &portUnreachableClient{}
	uc := &HostUsecase{repo: repo, taskflow: runtime}
	owner := uuid.New()
	_, err := uc.ApplyPort(context.Background(), owner, &domain.ApplyPortReq{ID: "foreign", Port: 8080, WhiteList: []string{"127.0.0.1"}})
	if !errors.Is(err, errcode.ErrNotFound) {
		t.Fatal("create/update did not preserve permission denial")
	}
	err = uc.RecyclePort(context.Background(), owner, &domain.RecyclePortReq{ID: "foreign", ForwardID: uuid.NewString()})
	if !errors.Is(err, errcode.ErrNotFound) {
		t.Fatal("close did not preserve permission denial")
	}
	if repo.calls != 2 || runtime.called {
		t.Fatal("port mutation reached runtime before authorizing environment")
	}
}
