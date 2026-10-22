package runtimeadapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	"github.com/google/uuid"
)

type roundResolverFixture struct {
	calls int
	owner uuid.UUID
	pins  []taskflow.RoundSkillPin
}

func (r *roundResolverFixture) ResolveRoundSkills(_ context.Context, owner, _ uuid.UUID, id *uuid.UUID) (*taskflow.RoundSkillSnapshot, *taskflow.SkillMention, error) {
	r.calls++
	r.owner = owner
	var mention *taskflow.SkillMention
	if id != nil {
		mention = &taskflow.SkillMention{ID: *id, Name: "report", VersionID: r.pins[0].VersionID, Version: "v1"}
	}
	return &taskflow.RoundSkillSnapshot{Pins: append([]taskflow.RoundSkillPin{}, r.pins...)}, mention, nil
}

func TestContinuationFreezesRoundSkillsAndRetryDoesNotResolveAgain(t *testing.T) {
	l := testLedger(t)
	req := stageFixture(t, l)
	ctx := context.Background()
	if _, err := l.db.ExecContext(ctx, `UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	r := &roundResolverFixture{pins: []taskflow.RoundSkillPin{{SkillID: uuid.New(), VersionID: uuid.New(), Name: "report", Version: "v1", S3Key: "report.zip", SHA256: "hash", Source: "personal"}}}
	c := &Client{ledger: l, skillResolver: r, engines: map[string]*Engine{"node": {}}}
	task := &taskflow.Task{ID: req.ID, ClientMessageID: uuid.NewString(), MentionedSkillID: &r.pins[0].SkillID}
	client := &taskClient{c: c}
	if err := client.Continue(ctx, taskflow.TaskReq{Task: task}); err != nil {
		t.Fatal(err)
	}
	var commandID string
	var sealed []byte
	if err := l.db.QueryRowContext(ctx, `SELECT id,payload FROM runtime_commands WHERE task_id=$1 AND operation='task' ORDER BY turn DESC LIMIT 1`, req.ID).Scan(&commandID, &sealed); err != nil {
		t.Fatal(err)
	}
	plain, err := l.open(commandID, sealed)
	if err != nil {
		t.Fatal(err)
	}
	var round taskflow.CreateTaskReq
	if err = json.Unmarshal(plain, &round); err != nil {
		t.Fatal(err)
	}
	if round.UserText == nil || *round.UserText != "" || round.Text == "" || round.SkillMention == nil || len(round.RoundSkills.Pins) != 1 {
		t.Fatalf("skill-only round not recorded: %#v", round)
	}
	env, err := l.EnvironmentForTask(ctx, req.ID.String())
	if err != nil || r.owner.String() != env.OwnerID {
		t.Fatal("operator rather than task owner was resolved")
	}
	r.pins = nil
	if err = client.Continue(ctx, taskflow.TaskReq{Task: task}); err != nil || r.calls != 1 {
		t.Fatalf("retry re-resolved versions: %v calls=%d", err, r.calls)
	}
	task.Text = "changed"
	if err = client.Continue(ctx, taskflow.TaskReq{Task: task}); err == nil {
		t.Fatal("message identity reused for different content")
	}
	task.Text = "next"
	task.MentionedSkillID = nil
	task.ClientMessageID = uuid.NewString()
	if err = client.Continue(ctx, taskflow.TaskReq{Task: task}); err == nil || r.calls != 1 {
		t.Fatal("active round allowed a resource update")
	}
	if _, err = l.db.ExecContext(ctx, `UPDATE runtime_commands SET state='complete' WHERE task_id=$1`, req.ID); err != nil {
		t.Fatal(err)
	}
	if err = client.Continue(ctx, taskflow.TaskReq{Task: task}); err != nil || r.calls != 2 {
		t.Fatalf("next round did not refresh: %v", err)
	}
	chunks, err := l.Events(ctx, req.ID.String(), 0)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("duplicate echo: %v count=%d", err, len(chunks))
	}
	var echo struct {
		Content      []byte                 `json:"content"`
		SkillMention *taskflow.SkillMention `json:"skill_mention"`
	}
	if err = json.Unmarshal(chunks[0].Data, &echo); err != nil || len(echo.Content) != 0 || echo.SkillMention == nil {
		t.Fatal("execution instruction leaked into displayed input")
	}
	if len(round.RoundSkills.Pins) != 1 {
		t.Fatal("prior snapshot mutated")
	}
}

func TestAttachmentRetryIdentityIgnoresRenewedSigningOnly(t *testing.T) {
	a := []taskflow.Attachment{{Filename: "table.csv", URL: "https://objects.test/owned/table.csv?versionId=7&X-Amz-Signature=old"}}
	b := []taskflow.Attachment{{Filename: "table.csv", URL: "https://objects.test/owned/table.csv?X-Amz-Signature=new&versionId=7"}}
	if !sameInputAttachments(a, b) {
		t.Fatal("renewed signature changed message identity")
	}
	b[0].URL = "https://objects.test/owned/table.csv?versionId=8&X-Amz-Signature=new"
	if sameInputAttachments(a, b) {
		t.Fatal("different object version treated as same input")
	}
}
