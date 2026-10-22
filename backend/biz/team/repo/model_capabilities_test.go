package repo

import (
	"context"
	"testing"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/domain"
)

func TestTeamModelCapabilitiesPersistExplicitFalseAndPreserveOmittedValues(t *testing.T) {
	ctx := context.Background()
	client := newTeamRepoTestDB(t)
	teamID := createTeamRepoTestTeam(t, client)
	userID := createTeamRepoTestUser(t, client)
	createTeamRepoDefaultGroup(t, client, teamID)
	repo := &teamModelRepo{db: client}
	enabled, disabled := true, false
	for _, tc := range []struct {
		name                    string
		thinking, image         *bool
		wantThinking, wantImage bool
	}{
		{"defaults", nil, nil, true, false},
		{"reasoning_off_images_on", &disabled, &enabled, false, true},
		{"reasoning_on_images_off", &enabled, &disabled, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCapabilities := func(got *db.Model, err error, thinking, image bool) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				if got == nil {
					t.Fatal("missing model response")
				}
				response := new(domain.TeamModel).From(got)
				if response.ThinkingEnabled != thinking || response.SupportImage != image {
					t.Fatalf("capabilities = (%v, %v), want (%v, %v)", response.ThinkingEnabled, response.SupportImage, thinking, image)
				}
			}
			created, err := repo.Create(ctx, teamID, userID, &domain.AddTeamModelReq{
				Provider: "openai", APIKey: "test-key", BaseURL: "https://example.com/v1", Model: tc.name,
				InterfaceType: consts.InterfaceTypeOpenAIChat, ThinkingEnabled: tc.thinking, SupportImage: tc.image,
			})
			assertCapabilities(created, err, tc.wantThinking, tc.wantImage)
			// Editing unrelated fields must not re-enable an explicitly disabled capability.
			remark := "updated"
			updated, err := repo.Update(ctx, teamID, &domain.UpdateTeamModelReq{ModelID: created.ID, Remark: &remark})
			assertCapabilities(updated, err, tc.wantThinking, tc.wantImage)
			thinking, image := !tc.wantThinking, !tc.wantImage
			updated, err = repo.Update(ctx, teamID, &domain.UpdateTeamModelReq{
				ModelID: created.ID, ThinkingEnabled: &thinking, SupportImage: &image,
			})
			assertCapabilities(updated, err, thinking, image)
			stored, err := repo.Get(ctx, teamID, created.ID)
			assertCapabilities(stored, err, thinking, image)
		})
	}
}
