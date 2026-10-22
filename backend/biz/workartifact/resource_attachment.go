package workartifact

import (
	"context"
	"errors"
	"mime"
	"path"

	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/google/uuid"
)

// Reuse an authorized input version through the normal per-round attachment
// pipeline. ResolveInputs verifies permissions, exact bytes and package limits.
func (s *Service) ResourceAttachment(ctx context.Context, uid uuid.UUID, req ResourceAttachmentRequest) (ResourceAttachment, error) {
	var out ResourceAttachment
	if err := s.enabled(); err != nil {
		return out, err
	}
	if req.VersionID == uuid.Nil || req.TaskID == uuid.Nil {
		return out, errcode.ErrBadRequest
	}
	project, err := s.taskPermission(ctx, uid, req.TaskID)
	if err != nil {
		return out, err
	}
	attachments, snapshots, err := s.ResolveInputs(ctx, uid, project, []uuid.UUID{req.VersionID})
	if err != nil {
		return out, err
	}
	if len(attachments) != 1 || len(snapshots) != 1 {
		return out, errors.New("文件暂不可用，请重新选择")
	}
	contentType := mime.TypeByExtension(path.Ext(attachments[0].Filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// The authenticated asset route is the same durable URL used by ordinary
	// uploads. Admission validates its owner and dispatch signs it for the Guest.
	return ResourceAttachment{URL: attachments[0].URL, Filename: attachments[0].Filename, Size: snapshots[0].Size, MIMEType: contentType}, nil
}
