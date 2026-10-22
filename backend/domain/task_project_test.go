package domain

import (
	"encoding/json"
	"testing"

	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/google/uuid"
)

func TestTaskProjectResponseHasOneNullableAssociation(t *testing.T) {
	project := uuid.New()
	zero := uuid.Nil
	for _, id := range []*uuid.UUID{nil, &zero, &project} {
		base := &db.Task{ID: uuid.New()}
		pt := &db.ProjectTask{ProjectID: id, Edges: db.ProjectTaskEdges{Task: base}}
		base.Edges.ProjectTasks = []*db.ProjectTask{pt}
		for _, response := range []any{(&Task{}).From(base), (&ProjectTask{}).From(pt)} {
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err = json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			value, present := out["project_id"]
			if !present {
				t.Fatal("missing explicit nullable project field")
			}
			if id == nil || *id == uuid.Nil {
				if value != nil {
					t.Fatal("independent task has a project", value)
				}
			} else if value != id.String() {
				t.Fatal("project lost", value)
			}
			if extra, ok := out["extra"].(map[string]any); ok {
				if _, exists := extra["project_id"]; exists {
					t.Fatal("duplicate extra project field")
				}
			}
		}
	}
}
