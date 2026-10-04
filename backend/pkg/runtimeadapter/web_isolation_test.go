package runtimeadapter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/MonkeyCode/backend/config"
	"github.com/chaitin/MonkeyCode/backend/pkg/crypto"
	"github.com/google/uuid"
)

// Original HTTP authentication and authorization against the dedicated local
// PoC. The second account is a DB fixture because the baseline MemberManager
// implementation is absent. This does not certify member administration.
func TestWebAccessIsolation(t *testing.T) {
	state := os.Getenv("RUNTIME_WEB_STATE_DIR")
	environment := os.Getenv("RUNTIME_WEB_TEST_VM")
	if state == "" || environment == "" {
		t.Skip("set isolated Web state directory and test environment ID")
	}
	cfg, err := config.Init(filepath.Join(state, "web", "config", "server"))
	if err != nil {
		t.Fatal("cannot read isolated Web config")
	}
	dsn, err := url.Parse(cfg.Database.Master)
	if err != nil || dsn.Hostname() != "127.0.0.1" || dsn.Path != "/monkeycode_web_poc" || cfg.Server.BaseURL != "http://127.0.0.1:47420" || filepath.Base(state) != ".state" {
		t.Fatal("Web isolation test must target only the named local PoC")
	}
	db, err := sql.Open("postgres", cfg.Database.Master)
	if err != nil {
		t.Fatal("cannot open isolated Web DB")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var task string
	if err = db.QueryRowContext(ctx, `SELECT task_id FROM runtime_task_intents WHERE environment_id=$1`, environment).Scan(&task); err != nil {
		t.Fatal("named Web environment is not an admitted test task")
	}
	type account struct {
		ID       string `json:"id,omitempty"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	readAccount := func(name string) account {
		t.Helper()
		var a account
		data, err := os.ReadFile(filepath.Join(state, name))
		if err != nil || json.Unmarshal(data, &a) != nil || !strings.HasSuffix(a.Email, "@example.invalid") || a.Password == "" {
			t.Fatal("invalid isolated test account file")
		}
		return a
	}
	owner := readAccount("web-account.json")
	outsiderFile := filepath.Join(state, "web-outsider-account.json")
	if _, err = os.Stat(outsiderFile); os.IsNotExist(err) {
		a := account{ID: uuid.NewString(), Email: "runtime-outsider@example.invalid", Password: strings.ReplaceAll(uuid.NewString(), "-", "")[:22] + "_P1!"}
		if err = os.WriteFile(outsiderFile, mustJSON(a), 0600); err != nil {
			t.Fatal("cannot save isolated fixture credentials")
		}
	}
	outsider := readAccount("web-outsider-account.json")
	id, err := uuid.Parse(outsider.ID)
	if err != nil || id == uuid.Nil {
		t.Fatal("invalid isolated fixture user ID")
	}
	hashed, err := crypto.HashPassword(outsider.Password)
	if err != nil {
		t.Fatal("cannot hash isolated fixture password")
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO users(id,name,email,password,role,status,is_blocked,created_at,updated_at) VALUES($1,'Isolated access-denial fixture',$2,$3,'individual','active',false,now(),now()) ON CONFLICT(id) DO NOTHING`, id, outsider.Email, hashed); err != nil {
		t.Fatal("cannot create isolated fixture user")
	}
	var savedEmail, savedRole, savedHash string
	if err = db.QueryRowContext(ctx, `SELECT email,role,password FROM users WHERE id=$1`, id).Scan(&savedEmail, &savedRole, &savedHash); err != nil || savedEmail != outsider.Email || savedRole != "individual" || crypto.VerifyPassword(savedHash, outsider.Password) != nil {
		t.Fatal("isolated account fixture conflicts with stored user")
	}
	newClient := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	type result struct {
		Scenario string `json:"scenario"`
		Status   int    `json:"http_status"`
		Code     int    `json:"business_code"`
		Passed   bool   `json:"passed"`
	}
	request := func(client *http.Client, method, path string, data []byte) (int, int, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, cfg.Server.BaseURL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal("invalid isolated HTTP request")
		}
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("isolated HTTP request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			t.Fatal("cannot read isolated HTTP response")
		}
		var envelope struct {
			Code int `json:"code"`
		}
		code := -1
		if json.Unmarshal(body, &envelope) == nil {
			code = envelope.Code
		}
		return response.StatusCode, code, body
	}
	login := func(a account) *http.Client {
		t.Helper()
		client := newClient()
		status, code, _ := request(client, http.MethodPost, "/api/v1/users/password-login", mustJSON(map[string]string{"email": a.Email, "password": a.Password}))
		if status != 200 || code != 0 {
			t.Fatalf("original password login failed: HTTP %d, code %d", status, code)
		}
		return client
	}
	ownerClient, outsiderClient := login(owner), login(outsider)
	report := []result{}
	paths := map[string]string{
		"file-list":     "/api/v1/users/folders?" + url.Values{"id": {environment}, "path": {"/workspace"}}.Encode(),
		"environment":   "/api/v1/users/hosts/vms/" + environment,
		"terminal-list": "/api/v1/users/hosts/vms/" + environment + "/terminals",
		"task-detail":   "/api/v1/users/tasks/" + task,
		"task-history":  "/api/v1/users/tasks/rounds?id=" + task,
	}
	for scenario, path := range paths {
		status, code, _ := request(ownerClient, http.MethodGet, path, nil)
		if status != 200 || code != 0 {
			t.Fatalf("owner %s failed: HTTP %d, code %d", scenario, status, code)
		}
		status, code, _ = request(outsiderClient, http.MethodGet, path, nil)
		// Do not count generic server/database failures as authorization evidence.
		denied := status == 403 || code == 10001 || code == 10002 || code == 10201 || code == 10202
		if !denied {
			t.Fatalf("outsider %s did not receive an explicit access denial: HTTP %d, code %d", scenario, status, code)
		}
		report = append(report, result{scenario, status, code, true})
	}
	download := "/api/v1/users/files/download?" + url.Values{"id": {environment}, "path": {"/workspace/中文上传验收.txt"}}.Encode()
	status, _, body := request(ownerClient, http.MethodGet, download, nil)
	if status != 200 || !bytes.Equal(body, []byte("中文文件编辑验收\nWEB_EDIT_OK\n")) {
		t.Fatal("owner could not download the synthetic fixture")
	}
	status, code, _ := request(outsiderClient, http.MethodGet, download, nil)
	if status != 403 && code != 10100 && code != 10201 && code != 10002 {
		t.Fatalf("outsider download was not explicitly denied: HTTP %d, code %d", status, code)
	}
	report = append(report, result{"file-download", status, code, true})
	status, code, _ = request(newClient(), http.MethodGet, download, nil)
	if status != 401 {
		t.Fatalf("anonymous download was not denied: HTTP %d, code %d", status, code)
	}
	report = append(report, result{"anonymous-download", status, code, true})
	if err = os.WriteFile(filepath.Join(state, "web-isolation-report.json"), mustJSON(report), 0600); err != nil {
		t.Fatal("cannot save isolated acceptance report")
	}
	t.Log("original login, owner access, cross-user file/task/environment/terminal denial and anonymous download denial passed")
}
