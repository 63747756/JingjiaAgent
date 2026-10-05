package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func acpChunk(e *v2.RunEvent, update map[string]any) taskflow.TaskChunk {
	ts := time.Now().UnixNano()
	if e.CreatedAt != nil {
		ts = e.CreatedAt.AsTime().UnixNano()
	}
	return taskflow.TaskChunk{Event: "task-running", Kind: "acp_event", Timestamp: ts, Data: mustJSON(map[string]any{"update": update})}
}
func textUpdate(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

func (c *Client) ingestEvent(ctx context.Context, cmd Command, e *v2.RunEvent) error {
	if e == nil {
		return errors.New("runtime returned a missing event")
	}
	if e.Kind == v2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE {
		// Native completion repeats the final assistant text. Compare it with
		// already committed deltas, including after a worker restart.
		prior, err := c.ledger.textTail(ctx, cmd.ID, len(e.Text)+1)
		if err != nil {
			return err
		}
		tail := finalTextTail(prior, e.Text)
		if tail == "" {
			return nil
		}
		return c.ledger.Append(ctx, cmd, fmt.Sprint(e.Seq), acpChunk(e, textUpdate("agent_message_chunk", tail)))
	}
	if e.Kind != v2.RunEventKind_RUN_EVENT_KIND_AGENT_ACTIVITY {
		return nil
	}
	var interaction struct {
		Kind  string          `json:"kind"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(e.PayloadJson), &interaction) == nil && interaction.Kind == "tool_call" && interactionID.MatchString(interaction.ID) {
		var native nativeInteraction
		if json.Unmarshal(interaction.Input, &native) == nil && native.ID == interaction.ID && native.RunID == cmd.RunID && native.SessionID != "" && (native.Kind == "question" || native.Kind == "permission") {
			if interaction.Name == "jingjiaagent_permission_reply" {
				if native.Kind != "permission" || len(native.Questions) != 1 {
					return errors.New("invalid automatic permission reply")
				}
				request := taskflow.AskUserQuestionResponse{TaskId: cmd.TaskID, RequestId: native.ID, AnswersJson: string(mustJSON(map[string]string{native.Questions[0].Question: "允许一次"}))}
				return c.ledger.Append(ctx, cmd, "interaction-reply/"+native.ID, taskflow.TaskChunk{Event: "reply-question", Timestamp: time.Now().UnixNano(), Data: mustJSON(request)})
			}
			chunk := acpChunk(e, nil)
			chunk.Kind = "acp_ask_user_question"
			chunk.Data = mustJSON(map[string]any{"toolCall": map[string]any{"toolCallId": native.ID, "title": "question", "rawInput": interaction.Input}})
			return c.ledger.Append(ctx, cmd, "interaction/"+native.ID, chunk)
		}
	}
	updates, err := activityUpdates(e.PayloadJson)
	if err != nil {
		return err
	}
	for i, update := range updates {
		if err = c.ledger.Append(ctx, cmd, fmt.Sprintf("%d/%d", e.Seq, i), acpChunk(e, update)); err != nil {
			return err
		}
	}
	for _, update := range updates {
		if update["sessionUpdate"] == "tool_call_update" && update["status"] == "completed" {
			// Tool completion invalidates repository views through the existing
			// control channel. This does not claim to be a general filesystem watcher.
			chunk := acpChunk(e, nil)
			chunk.Event = "task-event"
			chunk.Kind = "repo_file_change"
			chunk.Data = mustJSON(map[string]any{})
			return c.ledger.Append(ctx, cmd, fmt.Sprintf("%d/repo", e.Seq), chunk)
		}
	}
	return nil
}

func (s *Ledger) textTail(ctx context.Context, command string, limit int) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chunk FROM runtime_events WHERE command_id=$1 AND source_key LIKE '%/%' AND chunk->>'kind'='acp_event' ORDER BY seq DESC`, command)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	parts := []string{}
	size := 0
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return "", err
		}
		var chunk taskflow.TaskChunk
		if err = json.Unmarshal(b, &chunk); err != nil {
			return "", err
		}
		var envelope struct {
			Update struct {
				Kind    string          `json:"sessionUpdate"`
				Content json.RawMessage `json:"content"`
			} `json:"update"`
		}
		if err = json.Unmarshal(chunk.Data, &envelope); err != nil {
			return "", err
		}
		if envelope.Update.Kind == "agent_message_chunk" {
			var content struct {
				Text string `json:"text"`
			}
			if err = json.Unmarshal(envelope.Update.Content, &content); err != nil {
				return "", err
			}
			parts = append(parts, content.Text)
			size += len(content.Text)
			if size >= limit {
				break
			}
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	var out strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		out.WriteString(parts[i])
	}
	return out.String(), nil
}

// Linear suffix/prefix reconciliation, preserving any final text never streamed.
func finalTextTail(streamed, final string) string {
	streamed = strings.TrimSpace(streamed)
	if strings.HasSuffix(streamed, strings.TrimSpace(final)) {
		return ""
	}
	if len(streamed) > len(final) {
		streamed = streamed[len(streamed)-len(final):]
	}
	combined := final + "\x00" + streamed
	failure := make([]int, len(combined))
	for i := 1; i < len(combined); i++ {
		n := failure[i-1]
		for n > 0 && combined[i] != combined[n] {
			n = failure[n-1]
		}
		if combined[i] == combined[n] {
			n++
		}
		failure[i] = n
	}
	n := failure[len(combined)-1]
	if n > len(final) || !strings.HasSuffix(streamed, final[:n]) {
		return final
	}
	return final[n:]
}

func activityUpdates(payload string) ([]map[string]any, error) {
	var e struct {
		Kind     string          `json:"kind"`
		Text     string          `json:"text"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		ToolKind string          `json:"toolKind"`
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Output   string          `json:"output"`
		Error    string          `json:"error"`
		OK       bool            `json:"ok"`
		Phase    string          `json:"phase"`
		Attempt  int             `json:"attempt"`
		Message  string          `json:"message"`
		Items    []struct {
			Text      string `json:"text"`
			Completed bool   `json:"completed"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(payload), &e) != nil || e.Kind == "" {
		return nil, errors.New("invalid runtime activity event")
	}
	var u map[string]any
	switch e.Kind {
	case "text_delta":
		u = textUpdate("agent_message_chunk", e.Text)
	case "reasoning_delta":
		u = textUpdate("agent_thought_chunk", e.Text)
	case "tool_call":
		if e.ID == "" {
			return nil, errors.New("runtime tool event has no ID")
		}
		u = map[string]any{"sessionUpdate": "tool_call", "toolCallId": e.ID, "title": e.Name, "kind": e.ToolKind, "status": e.Status}
		if len(e.Input) > 0 {
			u["rawInput"] = e.Input
		}
		// ACP separates creation and update; some providers repeat tool_call
		// with a completed state. Emit both with distinct durable source keys.
		update := map[string]any{}
		for k, v := range u {
			update[k] = v
		}
		update["sessionUpdate"] = "tool_call_update"
		return []map[string]any{u, update}, nil
	case "tool_result":
		if e.ID == "" {
			return nil, errors.New("runtime tool result has no ID")
		}
		status := "completed"
		if !e.OK {
			status = "failed"
		}
		output := e.Output
		if e.Error != "" {
			output = e.Error
		}
		u = map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": e.ID, "status": status, "rawOutput": output, "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": output}}}}
	case "todo":
		entries := []map[string]any{}
		for _, item := range e.Items {
			status := "pending"
			if item.Completed {
				status = "completed"
			}
			entries = append(entries, map[string]any{"content": item.Text, "status": status, "priority": "medium"})
		}
		u = map[string]any{"sessionUpdate": "plan", "entries": entries}
	case "retry":
		u = map[string]any{"sessionUpdate": "llm_call_retry", "attempt": e.Attempt, "message": e.Message}
	case "compaction":
		status := "started"
		if e.Phase == "end" {
			status = "ended"
		}
		u = map[string]any{"sessionUpdate": "compact_status", "status": status}
	default:
		return nil, nil // usage scope is not equivalent to product context occupancy.
	}
	return []map[string]any{u}, nil
}
