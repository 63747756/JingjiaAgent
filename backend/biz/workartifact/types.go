package workartifact

import (
	"github.com/google/uuid"
	"time"
)

type InputSnapshot struct {
	ResourceID uuid.UUID  `json:"resource_id"`
	VersionID  uuid.UUID  `json:"version_id"`
	Filename   string     `json:"filename"`
	SHA256     string     `json:"sha256"`
	Size       int64      `json:"size"`
	ProjectID  *uuid.UUID `json:"project_id,omitempty"`
}
type ResourceVersion struct {
	ID         uuid.UUID `json:"id"`
	ResourceID uuid.UUID `json:"resource_id"`
	Filename   string    `json:"filename"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
	StorageKey string    `json:"-"`
}
type Resource struct {
	ID               uuid.UUID         `json:"id"`
	OwnerID          uuid.UUID         `json:"owner_id"`
	ProjectID        *uuid.UUID        `json:"project_id,omitempty"`
	Filename         string            `json:"filename"`
	CurrentVersionID uuid.UUID         `json:"current_version_id"`
	Versions         []ResourceVersion `json:"versions"`
}
type RegisterResourceRequest struct {
	URL       string     `json:"url" validate:"required"`
	Filename  string     `json:"filename" validate:"required"`
	ProjectID *uuid.UUID `json:"project_id,omitempty"`
}

type ResourceAttachmentRequest struct {
	VersionID uuid.UUID `json:"version_id"`
	TaskID    uuid.UUID `json:"task_id"`
}

type ResourceAttachment struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MIMEType string `json:"mime_type"`
}
type ArtifactVersion struct {
	ID               uuid.UUID `json:"id"`
	ArtifactID       uuid.UUID `json:"artifact_id"`
	Version          int       `json:"version"`
	Filename         string    `json:"filename"`
	MIMEType         string    `json:"mime_type"`
	Size             int64     `json:"size"`
	SHA256           string    `json:"sha256"`
	ValidationStatus string    `json:"validation_status"`
	PreviewStatus    string    `json:"preview_status"`
	CreatedAt        time.Time `json:"created_at"`
	StorageKey       string    `json:"-"`
	PreviewKey       string    `json:"-"`
}
type Artifact struct {
	ID               uuid.UUID         `json:"id"`
	TaskID           uuid.UUID         `json:"task_id"`
	ProjectID        *uuid.UUID        `json:"project_id,omitempty"`
	Title            string            `json:"title"`
	Filename         string            `json:"filename"`
	CurrentVersionID uuid.UUID         `json:"current_version_id"`
	ValidationStatus string            `json:"validation_status"`
	PreviewStatus    string            `json:"preview_status"`
	CanEdit          bool              `json:"can_edit"`
	CurrentVersion   *ArtifactVersion  `json:"current_version,omitempty"`
	VersionCount     int               `json:"version_count"`
	Versions         []ArtifactVersion `json:"versions,omitempty"`
}

type ArtifactPageRequest struct {
	Limit  int
	Cursor string
}

type ArtifactPage struct {
	Items      []Artifact `json:"items"`
	HasMore    bool       `json:"has_more"`
	NextCursor string     `json:"next_cursor,omitempty"`
}
type Checkpoint struct {
	ID            uuid.UUID `json:"id"`
	TaskID        uuid.UUID `json:"task_id"`
	EnvironmentID string    `json:"environment_id"`
	Generation    int       `json:"generation"`
	State         string    `json:"state"`
	PayloadSHA256 string    `json:"payload_sha256"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	ArchiveKey    string    `json:"-"`
}
