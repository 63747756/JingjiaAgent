package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

type hostListRepoStub struct {
	domain.TeamHostRepo
	hosts []*db.Host
	err   error
}

func (r hostListRepoStub) List(context.Context, uuid.UUID) ([]*db.Host, error) {
	return r.hosts, r.err
}

type hostListRuntimeStub struct {
	taskflow.Clienter
	hosts *hostListOnlineStub
	vms   *hostListVMStub
}

func (r hostListRuntimeStub) Host() taskflow.Hoster                     { return r.hosts }
func (r hostListRuntimeStub) VirtualMachiner() taskflow.VirtualMachiner { return r.vms }

type hostListOnlineStub struct {
	taskflow.Hoster
	ids []string
}

func (h *hostListOnlineStub) IsOnline(_ context.Context, req *taskflow.IsOnlineReq[string]) (*taskflow.IsOnlineResp, error) {
	h.ids = req.IDs
	return &taskflow.IsOnlineResp{OnlineMap: map[string]bool{"active": true}}, nil
}

type hostListVMStub struct {
	taskflow.VirtualMachiner
	ids []string
}

func (v *hostListVMStub) IsOnline(_ context.Context, req *taskflow.IsOnlineReq[string]) (*taskflow.IsOnlineResp, error) {
	v.ids = req.IDs
	return &taskflow.IsOnlineResp{OnlineMap: map[string]bool{}}, nil
}

func TestTeamHostListSkipsMissingAndDeletedHosts(t *testing.T) {
	online, vms := &hostListOnlineStub{}, &hostListVMStub{}
	u := &TeamHostUsecase{
		repo: hostListRepoStub{hosts: []*db.Host{
			nil,
			{ID: "removed", DeletedAt: time.Now()},
			{ID: "active", Edges: db.HostEdges{Vms: []*db.VirtualMachine{{ID: "vm-active"}}}},
		}},
		taskflow: hostListRuntimeStub{hosts: online, vms: vms},
	}
	resp, err := u.List(context.Background(), &domain.TeamUser{Team: &domain.Team{ID: uuid.New()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hosts) != 1 || resp.Hosts[0].ID != "active" || resp.Hosts[0].Status != consts.HostStatusOnline {
		t.Fatalf("valid hosts must remain visible: %#v", resp)
	}
	if !reflect.DeepEqual(online.ids, []string{"active"}) || !reflect.DeepEqual(vms.ids, []string{"vm-active"}) {
		t.Fatalf("status lookup included invalid hosts: hosts=%v, vms=%v", online.ids, vms.ids)
	}
}

func TestTeamHostListReturnsEmptyArrayWithoutRuntimeForUnboundHosts(t *testing.T) {
	for _, hosts := range [][]*db.Host{nil, {nil}, {{ID: "removed", DeletedAt: time.Now()}}} {
		u := &TeamHostUsecase{repo: hostListRepoStub{hosts: hosts}}
		resp, err := u.List(context.Background(), &domain.TeamUser{Team: &domain.Team{ID: uuid.New()}})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(resp)
		if err != nil || string(data) != `{"hosts":[]}` {
			t.Fatalf("empty list must be JSON, got %s, err=%v", data, err)
		}
	}
}

func TestTeamHostListPreservesRepositoryError(t *testing.T) {
	want := errors.New("database unavailable")
	u := &TeamHostUsecase{repo: hostListRepoStub{err: want}}
	_, err := u.List(context.Background(), &domain.TeamUser{Team: &domain.Team{ID: uuid.New()}})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want %v", err, want)
	}
}
