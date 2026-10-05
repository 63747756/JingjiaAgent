package usecase

import "testing"

func TestExtractScene(t *testing.T) {
	cases := []struct {
		name        string
		eventKey    string
		isSubscribe bool
		want        string
	}{
		{
			name:        "subscribe with qrscene prefix",
			eventKey:    "qrscene_jingjiaagent:8VYkSx4q9rP2mN6a",
			isSubscribe: true,
			want:        "jingjiaagent:8VYkSx4q9rP2mN6a",
		},
		{
			name:        "subscribe with empty event key",
			eventKey:    "",
			isSubscribe: true,
			want:        "",
		},
		{
			name:        "subscribe with prefix only",
			eventKey:    "qrscene_",
			isSubscribe: true,
			want:        "",
		},
		{
			name:        "subscribe without qrscene prefix is returned as-is",
			eventKey:    "jingjiaagent:abc",
			isSubscribe: true,
			want:        "jingjiaagent:abc",
		},
		{
			name:        "SCAN event without prefix",
			eventKey:    "jingjiaagent:8VYkSx4q9rP2mN6a",
			isSubscribe: false,
			want:        "jingjiaagent:8VYkSx4q9rP2mN6a",
		},
		{
			name:        "SCAN event preserves qrscene prefix (only subscribe strips it)",
			eventKey:    "qrscene_jingjiaagent:abc",
			isSubscribe: false,
			want:        "qrscene_jingjiaagent:abc",
		},
		{
			name:        "SCAN event with empty key",
			eventKey:    "",
			isSubscribe: false,
			want:        "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractScene(tc.eventKey, tc.isSubscribe)
			if got != tc.want {
				t.Errorf("ExtractScene(%q, %v) = %q, want %q", tc.eventKey, tc.isSubscribe, got, tc.want)
			}
		})
	}
}
