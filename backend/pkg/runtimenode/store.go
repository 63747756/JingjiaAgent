// Package runtimenode owns persisted connection configuration. It does not grant
// task access: the original host/user/group predicates remain authoritative.
package runtimenode

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
)

type Material struct {
	Token       string `json:"token"`
	CA          string `json:"ca"`
	Certificate string `json:"certificate,omitempty"`
	// Imported installers only; newly enrolled nodes never upload their TLS key.
	LegacyKey string `json:"legacy_key,omitempty"`
}
type Record struct {
	Config    config.RuntimeNode
	Material  Material
	Name      string
	Accepting bool
	Phase     string
	Imported  bool
	Revision  int64
	Revoked   bool
	UpdatedAt time.Time
}
type encrypted struct {
	Config   config.RuntimeNode
	Material Material
}
type Store struct {
	db     *sql.DB
	cipher cipher.AEAD
}

func New(db *sql.DB, key []byte) (*Store, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	return &Store{db: db, cipher: a}, nil
}
func (s *Store) seal(r Record) ([]byte, error) {
	b, e := json.Marshal(encrypted{r.Config, r.Material})
	if e != nil {
		return nil, e
	}
	n := make([]byte, s.cipher.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	return s.cipher.Seal(n, n, b, []byte("runtime-node:"+r.Config.ID)), nil
}
func (s *Store) decode(id string, b []byte) (encrypted, error) {
	var v encrypted
	n := s.cipher.NonceSize()
	if len(b) < n {
		return v, errors.New("invalid node credential payload")
	}
	p, e := s.cipher.Open(nil, b[:n], b[n:], []byte("runtime-node:"+id))
	if e != nil {
		return v, errors.New("cannot decrypt node credentials")
	}
	e = json.Unmarshal(p, &v)
	return v, e
}

const columns = `node_id,name,payload,accepting,phase,imported,revision,revoked_at IS NOT NULL,updated_at`

type scanner interface{ Scan(...any) error }

func (s *Store) scan(row scanner) (Record, error) {
	var r Record
	var id string
	var b []byte
	e := row.Scan(&id, &r.Name, &b, &r.Accepting, &r.Phase, &r.Imported, &r.Revision, &r.Revoked, &r.UpdatedAt)
	if e != nil {
		return r, e
	}
	v, e := s.decode(id, b)
	if e != nil {
		return r, e
	}
	r.Config = v.Config
	r.Material = v.Material
	return r, nil
}
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	return s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM runtime_node_configs WHERE node_id=$1`, id))
}
func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+columns+` FROM runtime_node_configs ORDER BY created_at,node_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, e := s.scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Create(ctx context.Context, r Record) error {
	b, e := s.seal(r)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO runtime_node_configs(node_id,owner_id,team_id,name,endpoint,payload,accepting,phase,imported)
 VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9)`, r.Config.ID, r.Config.OwnerID, r.Config.TeamID, r.Name, r.Config.URL, b, r.Accepting, r.Phase, r.Imported)
	return e
}

// Import skips records before opening files. Restart cannot overwrite UI edits,
// resurrect revoked hosts, or require removed legacy credential paths.
func (s *Store) Import(ctx context.Context, nodes []config.RuntimeNode) error {
	for _, n := range nodes {
		if _, e := s.Get(ctx, n.ID); e == nil {
			continue
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if n.OwnerID == "" {
			if e := s.db.QueryRowContext(ctx, `SELECT user_id FROM hosts WHERE id=$1`, n.ID).Scan(&n.OwnerID); e != nil {
				return errors.New("legacy runtime node has no owner")
			}
		}
		r := Record{Config: n, Accepting: true, Phase: "ready", Imported: true, Name: n.ID}
		for _, f := range []struct {
			path string
			dst  *string
		}{{n.TokenFile, &r.Material.Token}, {n.CAFile, &r.Material.CA}, {n.InstallCertFile, &r.Material.Certificate}, {n.InstallKeyFile, &r.Material.LegacyKey}} {
			if f.path == "" {
				continue
			}
			b, e := os.ReadFile(f.path)
			if e != nil {
				return errors.New("cannot import legacy node credentials")
			}
			*f.dst = string(b)
		}
		r.Material.Token = strings.TrimSpace(r.Material.Token)
		// Also preserve a host already removed via the old product API.
		var removed bool
		_ = s.db.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM hosts WHERE id=$1`, n.ID).Scan(&removed)
		if e := s.Create(ctx, r); e != nil {
			if _, getErr := s.Get(ctx, n.ID); getErr != nil {
				return e
			}
		}
		if removed {
			_, e := s.db.ExecContext(ctx, `UPDATE runtime_node_configs SET revoked_at=now(),accepting=false WHERE node_id=$1`, n.ID)
			if e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Store) SaveMaterial(ctx context.Context, r Record) error {
	b, e := s.seal(r)
	if e != nil {
		return e
	}
	res, e := s.db.ExecContext(ctx, `UPDATE runtime_node_configs SET payload=$2,phase=$3,revision=revision+1,updated_at=now()
 WHERE node_id=$1 AND revision=$4 AND revoked_at IS NULL`, r.Config.ID, b, r.Phase, r.Revision)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e == nil && n != 1 {
		return errors.New("node configuration changed; retry")
	}
	return e
}
func (s *Store) Ready(ctx context.Context, id string) error {
	_, e := s.db.ExecContext(ctx, `UPDATE runtime_node_configs SET phase='ready',revision=revision+1,updated_at=now() WHERE node_id=$1 AND phase='verifying' AND revoked_at IS NULL`, id)
	return e
}
func (s *Store) SetAccepting(ctx context.Context, id string, accepting bool) error {
	_, e := s.db.ExecContext(ctx, `UPDATE runtime_node_configs SET accepting=$2,revision=revision+1,updated_at=now() WHERE node_id=$1 AND revoked_at IS NULL`, id, accepting)
	return e
}
func (s *Store) Revoke(ctx context.Context, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(707326030)`); e != nil {
		return e
	}
	var used bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_environments WHERE node_id=$1 AND state<>'deleted')`, id).Scan(&used); e != nil {
		return e
	}
	if used {
		return errors.New("宿主机仍有关联工作空间，请先停止接收新任务并处理关联任务")
	}
	if _, e = tx.ExecContext(ctx, `UPDATE runtime_node_configs SET accepting=false,revoked_at=now(),revision=revision+1,updated_at=now() WHERE node_id=$1 AND revoked_at IS NULL`, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hosts SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Summary(ctx context.Context, id string) (bool, int, error) {
	var online bool
	var count int
	e := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT ready AND last_seen_at>now()-interval '35 seconds' FROM runtime_nodes WHERE node_id=$1),false),
 (SELECT count(*) FROM runtime_environments WHERE node_id=$1 AND state<>'deleted')`, id).Scan(&online, &count)
	return online, count, e
}

type Resources struct {
	CPUAvailable    float64 `json:"cpu_available"`
	MemoryAvailable int64   `json:"memory_available"`
	Running         int     `json:"running"`
	Issue           string  `json:"issue,omitempty"`
}

func (s *Store) Resources(ctx context.Context, id string) (Resources, error) {
	var r Resources
	e := s.db.QueryRowContext(ctx, `SELECT COALESCE(n.last_error_code,''),
 GREATEST(0,COALESCE(p.cpu_limit_millis,0)-COALESCE((SELECT sum(cpu_millis) FROM runtime_reservations WHERE capacity_id=n.capacity_id AND active),0))/1000.0,
 GREATEST(0,COALESCE(p.memory_limit_bytes,0)-COALESCE((SELECT sum(memory_bytes) FROM runtime_reservations WHERE capacity_id=n.capacity_id AND active),0)),
 (SELECT count(DISTINCT c.environment_id) FROM runtime_commands c JOIN runtime_environments e ON e.id=c.environment_id WHERE e.node_id=$1 AND c.state IN ('waiting','pending','running','unknown','submitting') AND c.submission_started)
 FROM runtime_nodes n LEFT JOIN runtime_capacity_pools p ON p.id=n.capacity_id WHERE n.node_id=$1`, id).Scan(&r.Issue, &r.CPUAvailable, &r.MemoryAvailable, &r.Running)
	if errors.Is(e, sql.ErrNoRows) {
		e = nil
	}
	return r, e
}
