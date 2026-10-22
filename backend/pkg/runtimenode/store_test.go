package runtimenode

import (
	"bytes"
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestEndpointRejectsUnsafeTargets(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "::1", "169.254.169.254", "0.0.0.0", "localhost", "abc.localhost", "user@host", "host/path", "host?query"} {
		if _, e := Endpoint(h, 7443); e == nil {
			t.Errorf("accepted %s", h)
		}
	}
	for _, h := range []string{"10.0.0.10", "192.168.5.4", "node.internal", "fd00::10"} {
		if _, e := Endpoint(h, 7443); e != nil {
			t.Errorf("rejected %s: %v", h, e)
		}
	}
	if _, e := SafeDialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "7443")); e == nil {
		t.Fatal("dial bypasses IP validation")
	}
}
func TestCredentialsAreBoundToNodeAndEncrypted(t *testing.T) {
	s, e := New(new(sql.DB), bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	r := Record{Config: config.RuntimeNode{ID: "node"}, Material: Material{Token: "private-token-for-test"}}
	a, e := s.seal(r)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := s.seal(r)
	if bytes.Contains(a, []byte(r.Material.Token)) || bytes.Equal(a, b) {
		t.Fatal("credentials are not randomized ciphertext")
	}
	if _, e = s.decode("other", a); e == nil {
		t.Fatal("cross-node ciphertext accepted")
	}
	a[len(a)-1] ^= 1
	if _, e = s.decode("node", a); e == nil {
		t.Fatal("tampered credentials accepted")
	}
}
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("JINGJIAAGENT_RUNTIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN required")
	}
	admin, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "node_test_" + uuid.NewString()
	quoted := `"` + schema + `"`
	if _, e = admin.Exec("CREATE SCHEMA " + quoted); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := sql.Open("postgres", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP SCHEMA " + quoted + " CASCADE"); admin.Close() })
	_, e = db.Exec(`CREATE TABLE users(id uuid PRIMARY KEY);CREATE TABLE teams(id uuid PRIMARY KEY);CREATE TABLE hosts(id text PRIMARY KEY,deleted_at timestamptz);CREATE TABLE runtime_environments(id text PRIMARY KEY,node_id text,state text);`)
	if e != nil {
		t.Fatal(e)
	}
	migration, e := os.ReadFile("../../migration/000049_dynamic_runtime_nodes.up.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(migration)); e != nil {
		t.Fatal(e)
	}
	owner := uuid.NewString()
	if _, e = db.Exec(`INSERT INTO users VALUES($1)`, owner); e != nil {
		t.Fatal(e)
	}
	s, e := New(db, bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	return s, owner
}
func TestImportIsOneTimeAndRevokeFencesWorkspaces(t *testing.T) {
	s, owner := testStore(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "token")
	os.WriteFile(file, []byte("legacy-secret"), 0600)
	n := config.RuntimeNode{ID: uuid.NewString(), URL: "https://node.test:7443", OwnerID: owner, TokenFile: file}
	if e := s.Import(ctx, []config.RuntimeNode{n}); e != nil {
		t.Fatal(e)
	}
	r, e := s.Get(ctx, n.ID)
	if e != nil || r.Material.Token != "legacy-secret" || !r.Imported {
		t.Fatal("import failed", e)
	}
	if e = s.SetAccepting(ctx, n.ID, false); e != nil {
		t.Fatal(e)
	}
	os.Remove(file)
	if e = s.Import(ctx, []config.RuntimeNode{n}); e != nil {
		t.Fatal("restart required old credential file", e)
	}
	r, _ = s.Get(ctx, n.ID)
	if r.Accepting {
		t.Fatal("import overwrote disabled state")
	}
	_, e = s.db.Exec(`INSERT INTO hosts VALUES($1,NULL);`, n.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(`INSERT INTO runtime_environments VALUES('workspace',$1,'running')`, n.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Revoke(ctx, n.ID); e == nil {
		t.Fatal("revoked a host with workspaces")
	}
	if _, e = s.db.Exec(`UPDATE hosts SET deleted_at=now() WHERE id=$1`, n.ID); e == nil {
		t.Fatal("legacy delete bypassed fence")
	}
	s.db.Exec(`UPDATE runtime_environments SET state='deleted'`)
	if e = s.Revoke(ctx, n.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Import(ctx, []config.RuntimeNode{n}); e != nil {
		t.Fatal(e)
	}
	r, _ = s.Get(ctx, n.ID)
	if !r.Revoked {
		t.Fatal("import resurrected revoked node")
	}
}
