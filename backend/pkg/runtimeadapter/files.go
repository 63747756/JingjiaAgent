package runtimeadapter

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/chaitin/MonkeyCode/backend/pkg/taskflow"
)

//go:embed guest/files.py
var fileScript []byte

type fileClient struct{ c *Client }

// Both script and arguments are base64 alphabets. User paths/content never enter shell syntax.
func (e *Engine) files(ctx context.Context, sandbox string, req any, out any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	command := "python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString(fileScript) + "'))\" '" + base64.StdEncoding.EncodeToString(b) + "'"
	if len(command) > 120000 {
		return errors.New("Guest file request exceeds safe argument size")
	}
	text, err := e.execute(ctx, sandbox, command, 2<<20)
	if err != nil {
		return err
	}
	var response struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err = json.Unmarshal([]byte(text), &response); err != nil {
		return errors.New("invalid Guest file response")
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if out != nil {
		return json.Unmarshal(response.Data, out)
	}
	return nil
}
func (f *fileClient) route(ctx context.Context, req taskflow.FileReq) (Environment, *Engine, taskflow.FileManager, error) {
	env, n, err := f.c.environment(ctx, req.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if f.c.legacy == nil {
			return env, nil, nil, ErrLegacyUnavailable
		}
		return env, nil, f.c.legacy.FileManager(), nil
	}
	if err != nil {
		return env, nil, nil, err
	}
	if err = authorize(env, req.UserID, ""); err != nil {
		return env, nil, nil, err
	}
	if env.SandboxID == "" {
		return env, nil, nil, errors.New("sandbox is not prepared")
	}
	return env, n, nil, nil
}
func (f *fileClient) Operate(ctx context.Context, req taskflow.FileReq) ([]*taskflow.File, error) {
	env, n, old, err := f.route(ctx, req)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old.Operate(ctx, req)
	}
	if req.Operate == taskflow.FileOpSave {
		ch := make(chan []byte, 1)
		ch <- []byte(req.Content)
		close(ch)
		return []*taskflow.File{}, f.Upload(ctx, req, ch)
	}
	var out []*taskflow.File
	err = n.files(ctx, env.SandboxID, map[string]any{"op": req.Operate, "path": req.Path, "source": req.Source, "target": req.Target}, &out)
	if out == nil {
		out = []*taskflow.File{}
	}
	return out, err
}
func (f *fileClient) Download(ctx context.Context, req taskflow.FileReq, fn func(uint64, []byte) error) error {
	env, n, old, err := f.route(ctx, req)
	if err != nil {
		return err
	}
	if old != nil {
		return old.Download(ctx, req, fn)
	}
	var stat struct {
		Size      uint64  `json:"size"`
		Signature []int64 `json:"signature"`
	}
	if err = n.files(ctx, env.SandboxID, map[string]any{"op": "stat", "path": req.Path}, &stat); err != nil {
		return err
	}
	if err = fn(stat.Size, nil); err != nil {
		return err
	}
	for offset := uint64(0); offset < stat.Size; {
		var chunk struct {
			Data []byte `json:"data"`
		}
		if err = n.files(ctx, env.SandboxID, map[string]any{"op": "read", "path": req.Path, "offset": offset, "length": 32768, "signature": stat.Signature}, &chunk); err != nil {
			return err
		}
		if len(chunk.Data) == 0 || uint64(len(chunk.Data)) > stat.Size-offset {
			return errors.New("Guest file size mismatch")
		}
		if err = fn(0, chunk.Data); err != nil {
			return err
		}
		offset += uint64(len(chunk.Data))
	}
	return nil
}
func (f *fileClient) Upload(ctx context.Context, req taskflow.FileReq, data <-chan []byte) error {
	return f.upload(ctx, req, data, nil, false)
}
func (f *fileClient) upload(ctx context.Context, req taskflow.FileReq, data <-chan []byte, mode *uint32, parents bool) error {
	env, n, old, err := f.route(ctx, req)
	if err != nil {
		return err
	}
	if old != nil {
		return old.Upload(ctx, req, data)
	}
	var begin struct {
		Temp string `json:"temp"`
	}
	if err = n.files(ctx, env.SandboxID, map[string]any{"op": "begin", "path": req.Path, "parents": parents}, &begin); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = n.files(cleanup, env.SandboxID, map[string]any{"op": "abort", "path": req.Path, "temp": begin.Temp}, nil)
		}
	}()
	offset := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b, ok := <-data:
			if !ok {
				if err = ctx.Err(); err != nil {
					return err
				}
				err = n.files(ctx, env.SandboxID, map[string]any{"op": "commit", "path": req.Path, "temp": begin.Temp, "size": offset, "mode": mode}, nil)
				committed = err == nil
				return err
			}
			for len(b) > 0 {
				size := min(len(b), 32768)
				if err = n.files(ctx, env.SandboxID, map[string]any{"op": "write", "path": req.Path, "temp": begin.Temp, "offset": offset, "data": b[:size]}, nil); err != nil {
					return err
				}
				offset += uint64(size)
				b = b[size:]
			}
		}
	}
}
