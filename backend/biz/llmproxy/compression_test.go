package llmproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func gzipResponse(t *testing.T, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Check the complete HTTP path, both consumers, exact token arithmetic, and
// unchanged response content. Explicit caller compression caused the regression.
func TestProxyCompressionRecordsContextAndConsumption(t *testing.T) {
	for _, tc := range []struct {
		name, path, body      string
		stream                bool
		input, output, cached uint64
	}{
		{"chat-stream", "/v1/chat/completions", "data: {\"id\":\"probe\",\"usage\":{\"prompt_tokens\":33,\"completion_tokens\":16,\"prompt_tokens_details\":{\"cached_tokens\":10},\"completion_tokens_details\":{\"reasoning_tokens\":8}}}\n\ndata: [DONE]\n\n", true, 33, 16, 10},
		{"chat-json", "/v1/chat/completions", `{"id":"probe","usage":{"prompt_tokens":33,"completion_tokens":16}}`, false, 33, 16, 0},
		{"responses-stream", "/v1/responses", "event: response.completed\ndata: {\"response\":{\"id\":\"probe\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":30}}}}\n\n", true, 100, 20, 30},
		{"responses-json", "/v1/responses", `{"id":"probe","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":30}}}`, false, 100, 20, 30},
		{"anthropic-json", "/v1/messages", `{"id":"probe","usage":{"input_tokens":7,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`, false, 12, 5, 3},
		{"anthropic-stream", "/v1/messages", "event: message_start\ndata: {\"message\":{\"id\":\"probe\",\"usage\":{\"input_tokens\":7,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2}}}\n\nevent: message_delta\ndata: {\"usage\":{\"output_tokens\":5}}\n\n", true, 12, 5, 3},
	} {
		for _, encoding := range []string{"plain", "gzip-transport", "gzip-fallback"} {
			t.Run(tc.name+"/"+encoding, func(t *testing.T) {
				data := []byte(tc.body)
				if encoding != "plain" {
					data = gzipResponse(t, tc.body)
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
						t.Error("forwarded caller encoding")
					}
					if encoding != "plain" {
						w.Header().Set("Content-Encoding", "gzip")
					}
					w.Header().Set("Content-Type", "application/json")
					if tc.stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					w.Header().Set("Content-Length", fmt.Sprint(len(data)))
					_, _ = w.Write(data)
				}))
				defer upstream.Close()
				client := newProxyTestDB(t)
				key, _, taskID, modelID := seedProxyModelWithTask(t, client, upstream.URL+"/v1")
				if err := client.Model.UpdateOneID(uuid.MustParse(modelID)).SetContextLimit(200000).Exec(context.Background()); err != nil {
					t.Fatal(err)
				}
				usage := &usageRecorderStub{}
				contextUsage := &contextUsageFixture{}
				proxy := NewProxy(client, slog.New(slog.NewTextHandler(io.Discard, nil)), WithUsageRecorder(usage), WithContextUsageRecorder(contextUsage))
				if encoding == "gzip-fallback" {
					transport := &http.Transport{DisableCompression: true}
					defer transport.CloseIdleConnections()
					proxy.proxy.Transport = transport
				}
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(`{"model":"gpt-4o","stream":%t}`, tc.stream)))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Accept-Encoding", "gzip, deflate, br")
				response := httptest.NewRecorder()
				proxy.ServeHTTP(response, req)
				if response.Code != 200 || response.Body.String() != tc.body {
					t.Fatalf("response %d: %q", response.Code, response.Body.String())
				}
				if response.Header().Get("Content-Encoding") != "" {
					t.Fatal("stale encoding header")
				}
				events := usage.waitForEvents(t, 1)
				if len(events) != 1 {
					t.Fatalf("duplicate usage: %+v", events)
				}
				event := events[0]
				if event.TaskID != taskID || event.RequestID != "probe" || event.InputTokens != tc.input || event.OutputTokens != tc.output || event.CachedTokens != tc.cached || event.TotalTokens != tc.input+tc.output {
					t.Fatalf("consumption: %+v", event)
				}
				if contextUsage.calls != 1 || contextUsage.used != tc.input+tc.output || contextUsage.size != 200000 {
					t.Fatalf("context: %+v", contextUsage)
				}
			})
		}
	}
}

func TestProxyDecodesCompressedCredentialErrors(t *testing.T) {
	p := NewProxy(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(context.WithValue(req.Context(), contextKey{}, &proxyContext{model: &modelContext{apiKey: "secret"}}))
	response := &http.Response{StatusCode: 400, Request: req, Header: http.Header{"Content-Encoding": []string{"gzip"}}, Body: io.NopCloser(bytes.NewReader(gzipResponse(t, "Authorization: Bearer secret")))}
	if err := p.modifyResponse(response); err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || strings.Contains(string(data), "secret") || response.Header.Get("Content-Encoding") != "" {
		t.Fatalf("unsafe error: %s %v", data, err)
	}
}

func TestProxyRejectsBrokenCompressedResponse(t *testing.T) {
	for _, encoding := range []string{"gzip", "br"} {
		response := &http.Response{Header: http.Header{"Content-Encoding": []string{encoding}}, Body: io.NopCloser(strings.NewReader("invalid"))}
		if err := decodeModelResponse(response); err == nil {
			t.Fatalf("accepted invalid %s", encoding)
		}
		response.Body.Close()
	}
}

func TestGzipResponseStreamsBeforeCompletion(t *testing.T) {
	pr, pw := io.Pipe()
	release := make(chan struct{})
	defer close(release)
	defer pr.Close()
	go func() {
		w := gzip.NewWriter(pw)
		_, _ = io.WriteString(w, "data: hello\n\n")
		_ = w.Flush()
		<-release
		_ = w.Close()
		_ = pw.Close()
	}()
	response := &http.Response{Header: http.Header{"Content-Encoding": []string{"gzip"}}, Body: pr}
	if err := decodeModelResponse(response); err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	read := make(chan error, 1)
	go func() {
		data := make([]byte, len("data: hello\n\n"))
		_, err := io.ReadFull(response.Body, data)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gzip response waited for completion")
	}
}
