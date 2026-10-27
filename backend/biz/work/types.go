package work

import (
	"github.com/63747756/jingjiaagent/backend/biz/workartifact"
	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/domain"
	"github.com/google/uuid"
	"time"
)

type Profile struct {
	ID                  uuid.UUID      `json:"id"`
	TeamID              uuid.UUID      `json:"team_id"`
	Name                string         `json:"name"`
	Description         string         `json:"description"`
	Instructions        string         `json:"instructions"`
	TaskPresets         []TaskPreset   `json:"task_presets"`
	IsDefault           bool           `json:"is_default"`
	Enabled             bool           `json:"enabled"`
	ModelID             uuid.UUID      `json:"model_id,omitempty"`
	HostID              string         `json:"host_id,omitempty"`
	ImageID             uuid.UUID      `json:"image_id,omitempty"`
	CliName             consts.CliName `json:"cli_name,omitempty"`
	RequiredSkillIDs    []string       `json:"required_skill_ids,omitempty"`
	MentionableSkillIDs []string       `json:"mentionable_skill_ids,omitempty"`
	Core                int            `json:"core,omitempty"`
	Memory              uint64         `json:"memory,omitempty"`
}

// TaskPreset is an administrator-authored task offered by an assistant.
// Icon is visual only; execution follows assistant instructions and user goals.
type TaskPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Enabled     bool   `json:"enabled"`
}
type ConfigResponse struct {
	Enabled  bool          `json:"enabled"`
	Profiles []Profile     `json:"profiles"`
	TeamIDs  []uuid.UUID   `json:"team_ids"`
	Teams    []domain.Team `json:"teams"`
}
type CreateRequest struct {
	Branch             string                  `json:"branch,omitempty"`
	ModelID            *uuid.UUID              `json:"model_id,omitempty"`
	MentionedSkillID   *uuid.UUID              `json:"mentioned_skill_id,omitempty"`
	Content            string                  `json:"content"`
	TeamID             *uuid.UUID              `json:"team_id,omitempty"`
	ProjectID          *uuid.UUID              `json:"project_id,omitempty"`
	IssueID            *uuid.UUID              `json:"issue_id,omitempty"`
	ProfileID          *uuid.UUID              `json:"profile_id,omitempty"`
	TaskPresetID       string                  `json:"task_preset_id,omitempty"`
	SkillIDs           []string                `json:"skill_ids,omitempty"`
	SkillSelection     *SkillSelection         `json:"skill_selection,omitempty"`
	ResourceVersionIDs []uuid.UUID             `json:"resource_version_ids,omitempty"`
	Attachments        []domain.TaskAttachment `json:"attachments,omitempty"`
	OutputRequirements string                  `json:"output_requirements,omitempty"`
	ClientRequestID    *uuid.UUID              `json:"client_request_id,omitempty"`
}

type IssueContextRequest struct {
	ProjectID uuid.UUID `query:"project_id" validate:"required"`
	IssueID   uuid.UUID `query:"issue_id" validate:"required"`
}

// A present selection is final (including an empty list), except platform mandates.
type SkillSelection struct {
	IDs []string `json:"ids"`
}
type SkillPin struct {
	SkillID   uuid.UUID `json:"skill_id"`
	VersionID uuid.UUID `json:"version_id"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	SHA256    string    `json:"sha256"`
	S3Key     string    `json:"-"`
}
type Metadata struct {
	ProfileID          uuid.UUID                    `json:"profile_id"`
	AssistantName      string                       `json:"assistant_name"`
	TaskPreset         *TaskPreset                  `json:"task_preset,omitempty"`
	TeamID             uuid.UUID                    `json:"team_id"`
	SkillPins          []SkillPin                   `json:"skill_pins"`
	InputResources     []workartifact.InputSnapshot `json:"input_resources"`
	OutputRequirements string                       `json:"output_requirements"`
	BusinessStatus     string                       `json:"business_status"`
	CreatedAt          time.Time                    `json:"created_at"`
}
type TaskView struct {
	*domain.ProjectTask
	Work Metadata `json:"work"`
}
type ListRequest struct {
	ProjectID *uuid.UUID `query:"project_id"`
	Page      int        `query:"page"`
	Limit     int        `query:"limit"`
	Unlinked  bool       `query:"unlinked"`
}
type ListResponse struct {
	Items   []TaskView `json:"items"`
	Page    int        `json:"page"`
	HasMore bool       `json:"has_more"`
}

type UnifiedTaskView struct {
	*domain.ProjectTask
	Work *Metadata `json:"work,omitempty"`
}

type UnifiedListResponse struct {
	Items   []UnifiedTaskView `json:"items"`
	Page    int               `json:"page"`
	HasMore bool              `json:"has_more"`
}
type ProfileRequest struct {
	ID uuid.UUID `param:"id" json:"-"`
	Profile
}
