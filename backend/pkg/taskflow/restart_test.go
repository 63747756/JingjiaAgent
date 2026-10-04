package taskflow

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestRestartBusinessMutationSurvivesInternalSerialization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutation *RestartBusinessMutation
	}{
		{
			name: "model switch",
			mutation: &RestartBusinessMutation{
				OwnerID: uuid.New(),
				ModelSwitch: &RestartModelSwitch{
					ID: uuid.New(), ModelID: uuid.New(),
				},
			},
		},
		{
			name: "resource selection and explicit clear",
			mutation: &RestartBusinessMutation{
				OwnerID: uuid.New(),
				ResourceSelection: &RestartResourceSelection{
					SkillIDs: []string{"authorized-skill"}, PluginIDs: []string{},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := RestartTaskReq{
				ID: uuid.New(), RequestId: "authorized-switch", LoadSession: true,
				BusinessMutation: tc.mutation,
			}
			payload, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var stored RestartTaskReq
			if err := json.Unmarshal(payload, &stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, request) {
				t.Fatalf("server-created durable restart metadata lost during serialization: got %+v, want %+v", stored, request)
			}
		})
	}
}
