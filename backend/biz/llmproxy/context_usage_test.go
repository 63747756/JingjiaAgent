package llmproxy

import (
	"context"
	"testing"
	"time"
)

type contextUsageFixture struct {
	used        uint64
	size, calls int
}

func (f *contextUsageFixture) RecordContextUsage(_ context.Context, _ string, size int, used uint64, _ time.Time) error {
	f.used = used
	f.size = size
	f.calls++
	return nil
}
func TestContextUsageIncludesCacheWithoutDoubleCounting(t *testing.T) {
	for _, tc := range []struct {
		usage usageResult
		want  uint64
	}{
		{usageResult{InputTokens: 1000, OutputTokens: 200, CachedTokens: 800}, 1200},
		{usageResult{InputTokens: 100, OutputTokens: 200, CacheReadInputTokens: 800, CacheCreationInputTokens: 400}, 1500},
	} {
		f := &contextUsageFixture{}
		p := NewProxy(nil, nil, WithContextUsageRecorder(f))
		p.recordUsage(context.Background(), &proxyContext{model: &modelContext{vmID: "vm", contextLimit: 128000}, startedAt: time.Now()}, tc.usage)
		if f.used != tc.want || f.size != 128000 || f.calls != 1 {
			t.Fatalf("usage %+v", f)
		}
	}
}
