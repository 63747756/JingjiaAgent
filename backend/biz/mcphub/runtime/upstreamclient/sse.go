package upstreamclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type sseReader struct {
	scanner *bufio.Scanner
	read    int
}

func newSSEReader(body io.Reader) *sseReader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxMCPResponse)
	return &sseReader{scanner: scanner}
}

// Read through comments/notifications, and return as soon as the matching
// JSON-RPC response arrives. Waiting for EOF hangs persistent SSE servers.
func (s *sseReader) response(id string) ([]byte, error) {
	for {
		_, data, err := s.next()
		if err != nil {
			return nil, err
		}
		messages := []json.RawMessage{data}
		if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
			if err := json.Unmarshal(data, &messages); err != nil {
				return nil, err
			}
		}
		for _, message := range messages {
			var rpc struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(message, &rpc); err != nil {
				return nil, err
			}
			if rpc.Method != "" {
				if len(rpc.ID) != 0 {
					return nil, errors.New("upstream requires unsupported client request")
				}
				continue
			}
			if matchesRPCID(rpc.ID, id) {
				return message, nil
			}
		}
	}
}

func (s *sseReader) next() (string, []byte, error) {
	var event string
	var data []string
	for s.scanner.Scan() {
		line := s.scanner.Text()
		s.read += len(line) + 1
		if s.read > maxMCPResponse {
			return "", nil, errors.New("upstream SSE exceeds size limit")
		}
		if line == "" {
			if len(data) > 0 {
				return event, []byte(strings.Join(data, "\n")), nil
			}
			event = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", nil, err
	}
	// SSE events require a blank line; an incomplete final event is discarded.
	return "", nil, io.EOF
}

func (s *mcpSession) openLegacy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.upstream.URL, nil)
	if err != nil {
		return err
	}
	applyMCPHeaders(req, s.upstream.Headers)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Del("Mcp-Session-Id")
	req.Header.Del("Mcp-Protocol-Version")
	resp, err := s.client.client.Do(req)
	if err != nil {
		return err
	}
	s.stream = resp.Body
	if resp.StatusCode != http.StatusOK {
		return httpStatusError(resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return errors.New("legacy upstream did not return SSE")
	}
	s.events = newSSEReader(resp.Body)
	event, data, err := s.events.next()
	if err != nil {
		return fmt.Errorf("read legacy MCP endpoint: %w", err)
	}
	if event != "endpoint" {
		return errors.New("legacy MCP endpoint event missing")
	}
	base, err := url.Parse(s.upstream.URL)
	if err != nil {
		return err
	}
	relative, err := url.Parse(string(data))
	if err != nil {
		return errors.New("invalid legacy MCP endpoint")
	}
	endpoint := base.ResolveReference(relative)
	// A server-provided endpoint is not permission to forward the user's
	// configured Authorization/other headers to another origin.
	if !strings.EqualFold(endpoint.Scheme, base.Scheme) || !strings.EqualFold(endpoint.Host, base.Host) || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("legacy MCP endpoint must use the configured origin")
	}
	if err := s.client.guard.ValidateURL(ctx, endpoint.String()); err != nil {
		return err
	}
	s.endpoint = endpoint.String()
	return nil
}
