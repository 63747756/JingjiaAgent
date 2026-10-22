package runtimeinstall

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/errcode"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimenode"
	"github.com/google/uuid"
)

type nodeManager interface {
	NodeStore() *runtimenode.Store
	RefreshNodes(context.Context) error
	SyncNodes(context.Context) error
}
type EnrollmentRequest struct {
	ID      string `json:"id" validate:"required,uuid"`
	Name    string `json:"name" validate:"required,max=128"`
	Address string `json:"address" validate:"required,max=253"`
	Port    int    `json:"port" validate:"gte=1024,lte=65535"`
}
type AcceptingRequest struct {
	Accepting bool `json:"accepting"`
}
type Enrollment struct {
	runtimenode.Resources
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Endpoint   string     `json:"endpoint"`
	Scope      string     `json:"scope"`
	Phase      string     `json:"phase"`
	Online     bool       `json:"online"`
	Accepting  bool       `json:"accepting"`
	Workspaces int        `json:"workspaces"`
	Command    string     `json:"command,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}
type ClaimRequest struct {
	GuestImage  string `json:"guest_image,omitempty"`
	Token       string `json:"token" validate:"required"`
	Certificate string `json:"certificate" validate:"required,max=16384"`
	Signature   string `json:"signature" validate:"required,max=8192"`
}
type ClaimResponse struct {
	Token string `json:"node_token"`
}
type RenewRequest struct {
	NodeID      string `json:"node_id" validate:"required,uuid"`
	Certificate string `json:"certificate" validate:"required,max=16384"`
	Signature   string `json:"signature" validate:"required,max=8192"`
}

func (s *Service) manager() (nodeManager, error) {
	m, ok := s.confirmation.(nodeManager)
	if !ok || m.NodeStore() == nil {
		return nil, errcode.ErrRuntimeNodeInstaller
	}
	return m, nil
}
func (s *Service) scoped(ctx context.Context, actor, team, id string) (runtimenode.Record, error) {
	m, e := s.manager()
	if e != nil {
		return runtimenode.Record{}, e
	}
	r, e := m.NodeStore().Get(ctx, id)
	if e != nil {
		return r, errcode.ErrRuntimeInstallScope
	}
	if r.Revoked || r.Config.TeamID != team || (team == "" && r.Config.OwnerID != actor) {
		return r, errcode.ErrRuntimeInstallScope
	}
	e = s.auth.AuthorizeRuntimeInstall(ctx, actor, team, r.Config)
	return r, e
}
func (s *Service) view(ctx context.Context, r runtimenode.Record) (Enrollment, error) {
	m, e := s.manager()
	if e != nil {
		return Enrollment{}, e
	}
	online, count, e := m.NodeStore().Summary(ctx, r.Config.ID)
	if e != nil {
		return Enrollment{}, e
	}
	resources, e := m.NodeStore().Resources(ctx, r.Config.ID)
	scope := "personal"
	if r.Config.TeamID != "" {
		scope = "team"
	}
	return Enrollment{Resources: resources, ID: r.Config.ID, Name: r.Name, Endpoint: r.Config.URL, Scope: scope, Phase: r.Phase, Online: online, Accepting: r.Accepting, Workspaces: count}, e
}
func (s *Service) Enroll(ctx context.Context, actor, team string, req EnrollmentRequest) (Enrollment, error) {
	m, e := s.manager()
	if e != nil {
		return Enrollment{}, e
	}
	if !s.available() {
		return Enrollment{}, errcode.ErrRuntimeNodeInstaller
	}
	if id, e := uuid.Parse(req.ID); e != nil || id == uuid.Nil || id.String() != req.ID {
		return Enrollment{}, errcode.ErrBadRequest
	}
	endpoint, e := runtimenode.Endpoint(req.Address, req.Port)
	if e != nil {
		return Enrollment{}, errcode.ErrBadRequest.Wrap(e)
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 384 {
		return Enrollment{}, errcode.ErrBadRequest
	}
	r, e := m.NodeStore().Get(ctx, req.ID)
	if errors.Is(e, sql.ErrNoRows) {
		secret := make([]byte, 32)
		if _, e = rand.Read(secret); e != nil {
			return Enrollment{}, e
		}
		r = runtimenode.Record{Config: config.RuntimeNode{ID: req.ID, Name: strings.TrimSpace(req.Name), URL: endpoint, OwnerID: actor, TeamID: team, Install: true, InstallListen: net.JoinHostPort("0.0.0.0", strconv.Itoa(req.Port)), GuestImage: s.manifest.GuestImage}, Name: strings.TrimSpace(req.Name), Accepting: true, Phase: "waiting", Material: runtimenode.Material{Token: hex.EncodeToString(secret)}}
		if e = s.auth.AuthorizeRuntimeInstall(ctx, actor, team, r.Config); e != nil {
			return Enrollment{}, e
		}
		if e = m.NodeStore().Create(ctx, r); e != nil {
			return Enrollment{}, errcode.ErrBadRequest.Wrap(errors.New("宿主机地址已绑定，或绑定信息发生冲突，请刷新列表"))
		}
	} else if e != nil {
		return Enrollment{}, e
	}
	if r.Config.URL != endpoint {
		return Enrollment{}, errcode.ErrBadRequest.Wrap(errors.New("绑定请求已存在，不能改变目标地址"))
	}
	return s.EnrollmentCommand(ctx, actor, team, req.ID)
}
func (s *Service) EnrollmentCommand(ctx context.Context, actor, team, id string) (Enrollment, error) {
	r, e := s.scoped(ctx, actor, team, id)
	if e != nil {
		return Enrollment{}, e
	}
	if r.Imported {
		return Enrollment{}, errcode.ErrBadRequest.Wrap(errors.New("原有宿主机已迁移，继续使用当前安装；无需重复绑定"))
	}
	out, e := s.view(ctx, r)
	if e != nil {
		return out, e
	}
	token := uuid.NewString()
	value, _ := json.Marshal(ticket{Actor: actor, Team: team, Node: id, Phase: "issued"})
	// Regeneration revokes the old bootstrap ticket, including its download URL.
	_, e = s.redis.Eval(ctx, `local old=redis.call('GET',KEYS[1]); if old then redis.call('DEL',old) end; redis.call('SET',KEYS[1],KEYS[2],'EX',7200); redis.call('SET',KEYS[2],ARGV[1],'EX',7200); return 1`, []string{"jingjiaagent:node-install:" + id, ticketKey(token)}, string(value)).Result()
	if e != nil {
		return out, e
	}
	out.Command = s.commandURL(token)
	expiry := time.Now().Add(2 * time.Hour)
	out.ExpiresAt = &expiry
	return out, nil
}
func (s *Service) commandURL(token string) string {
	u, _ := url.Parse(s.platformURL())
	u = u.JoinPath("/api/v1/users/hosts/install")
	u.RawQuery = url.Values{"token": {token}}.Encode()
	return "bash -c " + shellQuote(fmt.Sprintf(`set -euo pipefail; script=$(curl --fail --silent --show-error --max-time 60 %s); bash -c "$script"`, shellQuote(u.String())))
}
func (s *Service) Enrollments(ctx context.Context, actor, team string) ([]Enrollment, error) {
	m, e := s.manager()
	if e != nil {
		return nil, e
	}
	records, e := m.NodeStore().List(ctx)
	if e != nil {
		return nil, e
	}
	out := []Enrollment{}
	for _, r := range records {
		if r.Revoked || r.Config.TeamID != team || (team == "" && r.Config.OwnerID != actor) {
			continue
		}
		if _, e = s.scoped(ctx, actor, team, r.Config.ID); e != nil {
			continue
		}
		v, e := s.view(ctx, r)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) UpdateEnrollment(ctx context.Context, actor, team, id string, accepting bool) (Enrollment, error) {
	r, e := s.scoped(ctx, actor, team, id)
	if e != nil {
		return Enrollment{}, e
	}
	m, _ := s.manager()
	if e = m.NodeStore().SetAccepting(ctx, id, accepting); e != nil {
		return Enrollment{}, e
	}
	_ = m.RefreshNodes(ctx)
	r.Accepting = accepting
	return s.view(ctx, r)
}
func (s *Service) RevokeEnrollment(ctx context.Context, actor, team, id string) error {
	if _, e := s.scoped(ctx, actor, team, id); e != nil {
		return e
	}
	m, _ := s.manager()
	if e := m.NodeStore().Revoke(ctx, id); e != nil {
		return errcode.ErrBadRequest.Wrap(e)
	}
	return m.RefreshNodes(ctx)
}
func parseCertificate(text, endpoint string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid node certificate")
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, e
	}
	u, e := url.Parse(endpoint)
	if e != nil {
		return nil, e
	}
	if e = cert.VerifyHostname(u.Hostname()); e != nil {
		return nil, e
	}
	if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) || cert.NotAfter.After(time.Now().Add(398*24*time.Hour)) {
		return nil, errors.New("invalid node certificate validity")
	}
	if e = cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); e != nil {
		return nil, e
	}
	return cert, nil
}
func verifySignature(cert *x509.Certificate, message, signature string) error {
	sig, e := base64.StdEncoding.DecodeString(signature)
	if e != nil {
		return e
	}
	h := sha256.Sum256([]byte(message))
	switch key := cert.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if ecdsa.VerifyASN1(key, h[:], sig) {
			return nil
		}
	case *rsa.PublicKey:
		if key.N.BitLen() >= 2048 {
			return rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig)
		}
	}
	return errors.New("node private-key proof failed")
}
func (s *Service) Claim(ctx context.Context, req ClaimRequest) (ClaimResponse, error) {
	_, node, e := s.load(ctx, req.Token, false)
	if e != nil {
		return ClaimResponse{}, e
	}
	m, e := s.manager()
	if e != nil {
		return ClaimResponse{}, e
	}
	r, e := m.NodeStore().Get(ctx, node.ID)
	if e != nil {
		return ClaimResponse{}, e
	}
	if r.Imported {
		return ClaimResponse{}, errcode.ErrRuntimeInstallScope
	}
	cert, e := parseCertificate(req.Certificate, node.URL)
	if e != nil {
		return ClaimResponse{}, errcode.ErrBadRequest.Wrap(e)
	}
	if e = verifySignature(cert, "jingjiaagent-enroll:"+req.Token+":"+node.ID, req.Signature); e != nil {
		return ClaimResponse{}, errcode.ErrRuntimeInstallScope
	}
	if r.Material.Certificate != "" && r.Material.Certificate != req.Certificate {
		return ClaimResponse{}, errcode.ErrBadRequest.Wrap(errors.New("此绑定已由另一台机器认领，请使用原机器，或创建新的绑定"))
	}
	installed := req.GuestImage
	if installed == "" {
		installed = s.manifest.GuestImage
	}
	if installed != s.manifest.GuestImage && installed != s.manifest.DockerImages[s.manifest.GuestImage] {
		return ClaimResponse{}, errcode.ErrBadRequest.Wrap(errors.New("安装的工作环境不属于已校验镜像包"))
	}
	if r.Config.InstalledGuestImage != "" && r.Config.InstalledGuestImage != installed {
		return ClaimResponse{}, errcode.ErrBadRequest.Wrap(errors.New("已绑定宿主机的镜像标识发生变化"))
	}
	if r.Material.Certificate == "" {
		r.Config.InstalledGuestImage = installed
		r.Material.Certificate = req.Certificate
		r.Material.CA = req.Certificate
		r.Phase = "verifying"
		if e = m.NodeStore().SaveMaterial(ctx, r); e != nil {
			return ClaimResponse{}, e
		}
	}
	if e = m.RefreshNodes(ctx); e != nil {
		return ClaimResponse{}, e
	}
	return ClaimResponse{Token: r.Material.Token}, nil
}
func (s *Service) Renew(ctx context.Context, token string, req RenewRequest) error {
	m, e := s.manager()
	if e != nil {
		return e
	}
	r, e := m.NodeStore().Get(ctx, req.NodeID)
	if e != nil || r.Revoked || r.Imported {
		return errcode.ErrRuntimeInstallScope
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(r.Material.Token)) != 1 {
		return errcode.ErrRuntimeInstallScope
	}
	old, _ := pem.Decode([]byte(r.Material.Certificate))
	if old == nil {
		return errcode.ErrRuntimeInstallScope
	}
	cert, err := x509.ParseCertificate(old.Bytes)
	if err != nil {
		return err
	}
	if _, err = parseCertificate(req.Certificate, r.Config.URL); err != nil {
		return errcode.ErrBadRequest.Wrap(err)
	}
	if err = verifySignature(cert, "jingjiaagent-renew:"+req.NodeID+":"+req.Certificate, req.Signature); err != nil {
		return errcode.ErrRuntimeInstallScope
	}
	if r.Material.Certificate == req.Certificate {
		return nil
	}
	r.Material.Certificate = req.Certificate
	r.Material.CA = req.Certificate
	return m.NodeStore().SaveMaterial(ctx, r)
}
