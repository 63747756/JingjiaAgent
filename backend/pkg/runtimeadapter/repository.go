package runtimeadapter

import (
	"context"
	"errors"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

func (t *taskClient) listFiles(ctx context.Context, r taskflow.RepoListFilesReq, e Environment, n *Engine) (*taskflow.RepoListFiles, error) {
	result := &taskflow.RepoListFiles{TaskId: r.TaskId, RequestId: r.RequestId, Path: r.Path, Files: []*taskflow.RepoFileInfo{}}
	err := n.files(ctx, e.SandboxID, map[string]any{"op": "repo_list", "path": r.Path, "glob_pattern": r.GlobPattern, "include_hidden": r.IncludeHidden}, &result.Files)
	if err != nil {
		return nil, err
	}
	result.Success = true
	return result, nil
}

func (t *taskClient) readFile(ctx context.Context, r taskflow.RepoReadFileReq, e Environment, n *Engine) (*taskflow.RepoReadFile, error) {
	var stat struct {
		Size      int64   `json:"size"`
		Signature []int64 `json:"signature"`
	}
	if err := n.files(ctx, e.SandboxID, map[string]any{"op": "repo_stat", "path": r.Path}, &stat); err != nil {
		return nil, err
	}
	offset, length := int64(0), int64(1<<20)
	if r.Offset != nil {
		offset = *r.Offset
	}
	if r.Length != nil {
		length = *r.Length
	}
	if offset < 0 || length < 0 {
		return nil, errors.New("invalid repository read range")
	}
	length = min(length, 1<<20)
	remaining := max(int64(0), stat.Size-offset)
	length = min(length, remaining)
	result := &taskflow.RepoReadFile{TaskId: r.TaskId, RequestId: r.RequestId, Path: r.Path, TotalSize: stat.Size, Offset: offset, Content: []byte{}, Success: true}
	for result.Length < length {
		var chunk struct {
			Data []byte `json:"data"`
		}
		if err := n.files(ctx, e.SandboxID, map[string]any{"op": "repo_read", "path": r.Path, "offset": offset + result.Length, "length": min(int64(32768), length-result.Length), "signature": stat.Signature}, &chunk); err != nil {
			return nil, err
		}
		if len(chunk.Data) == 0 || int64(len(chunk.Data)) > length-result.Length {
			return nil, errors.New("repository file size mismatch")
		}
		result.Content = append(result.Content, chunk.Data...)
		result.Length += int64(len(chunk.Data))
	}
	result.IsTruncated = result.Length < remaining
	return result, nil
}
