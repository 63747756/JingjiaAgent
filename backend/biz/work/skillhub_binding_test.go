package work

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHubBindingClosesTemporarySessionAndChecksCleanup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		logoutCode int
		location   string
		tokenError bool
		badLogin   bool
		cancel     bool
		wantError  bool
	}{
		{name: "spring redirect", logoutCode: 302, location: "/"},
		{name: "no content", logoutCode: 204},
		{name: "token rejected", logoutCode: 302, location: "/", tokenError: true, wantError: true},
		{name: "malformed login response", logoutCode: 204, badLogin: true, wantError: true},
		{name: "cancelled request", logoutCode: 204, cancel: true, wantError: true},
		{name: "logout not found", logoutCode: 404, wantError: true},
		{name: "logout forbidden", logoutCode: 403, wantError: true},
		{name: "logout unavailable", logoutCode: 503, wantError: true},
		{name: "unexpected redirect", logoutCode: 302, location: "/login", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var active atomic.Bool
			var logouts atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, user, _ := hubUploadFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/methods":
					http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "before", Path: "/"})
					io.WriteString(w, `{"code":0,"data":[{"provider":"local","methodType":"PASSWORD"}]}`)
				case "/api/v1/auth/local/login":
					if r.Header.Get("X-XSRF-TOKEN") != "before" {
						t.Error("missing initial CSRF token")
					}
					active.Store(true)
					http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "test-session", Path: "/"})
					http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "after", Path: "/"})
					if tc.badLogin {
						io.WriteString(w, `{"code":0,`)
					} else {
						io.WriteString(w, `{"code":0,"data":{"userId":"100","displayName":"Review account"}}`)
					}
				case "/api/v1/tokens":
					if tc.cancel {
						cancel()
					}
					if tc.tokenError {
						w.WriteHeader(403)
						io.WriteString(w, `{"code":403,"msg":"token rejected"}`)
						return
					}
					io.WriteString(w, `{"code":0,"data":{"id":123,"token":"test-token"}}`)
				case "/api/v1/auth/logout":
					logouts.Add(1)
					cookie, err := r.Cookie("SESSION")
					if err != nil || cookie.Value != "test-session" || r.Header.Get("X-XSRF-TOKEN") != "after" {
						t.Error("cleanup must send the temporary session and refreshed CSRF token")
					}
					if tc.location == "/" || tc.logoutCode == 204 {
						active.Store(false)
						http.SetCookie(w, &http.Cookie{Name: "SESSION", MaxAge: -1, Path: "/"})
					}
					if tc.location != "" {
						w.Header().Set("Location", tc.location)
					}
					w.WriteHeader(tc.logoutCode)
				default:
					t.Errorf("unexpected request/redirect: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			if _, err := s.store.db.Exec(`ALTER TABLE work_skillhub_bindings ADD COLUMN remote_user_id text, ADD COLUMN remote_name text, ADD COLUMN token_id bigint, ADD COLUMN updated_at timestamptz DEFAULT now(); DELETE FROM work_skillhub_bindings;`); err != nil {
				t.Fatal(err)
			}
			out, err := s.BindHub(ctx, user, HubBindRequest{Username: "review", Password: "test-only"})
			if (err != nil) != tc.wantError {
				t.Fatalf("binding result: %+v %v", out, err)
			}
			if logouts.Load() != 1 {
				t.Fatalf("cleanup calls=%d", logouts.Load())
			}
			if tc.location == "/" || tc.logoutCode == 204 {
				if active.Load() {
					t.Fatal("temporary session remained active")
				}
			} else {
				var input *skillValidationError
				if !errors.As(err, &input) || !strings.Contains(err.Error(), "注销失败") {
					t.Fatalf("cleanup failure was not exposed: %v", err)
				}
			}
			var bindings int
			if queryErr := s.store.db.QueryRow(`SELECT count(*) FROM work_skillhub_bindings`).Scan(&bindings); queryErr != nil {
				t.Fatal(queryErr)
			}
			if tc.wantError {
				if bindings != 0 {
					t.Fatal("failed binding was persisted")
				}
			} else if bindings != 1 || !out.Bound || out.Account != "Review account" {
				t.Fatalf("binding was not saved: %+v count=%d", out, bindings)
			}
		})
	}
}
