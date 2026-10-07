// Package adldap authenticates a single Active Directory over verified LDAPS.
package adldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-ldap/ldap/v3"
)

var (
	ErrInvalidCredentials = errors.New("AD authentication failed")
	ErrUnavailable        = errors.New("AD authentication service unavailable")
	ErrInvalidConfig      = errors.New("invalid AD configuration")
)

const (
	dialTimeout      = 3 * time.Second
	operationTimeout = 5 * time.Second
	requestTimeout   = 15 * time.Second
	memberOfChain    = "1.2.840.113556.1.4.1941"
)

type Config struct {
	URL             string
	BaseDN          string
	BindDN          string
	BindPassword    string
	CAPEM           string
	AllowedGroupDNs []string
}

type Department struct {
	GUID string
	DN   string
	Path string
}

type Profile struct {
	GUID        string
	Username    string
	DisplayName string
	Email       string
	Department  Department
}

type Directory interface {
	Authenticate(context.Context, Config, string, string) (*Profile, error)
	Test(context.Context, Config) error
}

// Client has no mutable connection or credentials. Its zero value is usable.
type Client struct{}

func New() *Client { return &Client{} }

var _ Directory = (*Client)(nil)

// ValidateConfig validates a draft without contacting the directory.
func ValidateConfig(cfg Config) error {
	_, _, err := connectionSettings(cfg)
	return err
}

func connectionSettings(cfg Config) (string, *tls.Config, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || u.Scheme != "ldaps" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, invalidConfig("a bare ldaps://host[:port] URL is required")
	}
	port := u.Port()
	if port == "" {
		port = "636"
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", nil, invalidConfig("invalid LDAPS port")
	}
	base, err := ldap.ParseDN(cfg.BaseDN)
	if err != nil || len(base.RDNs) == 0 {
		return "", nil, invalidConfig("a valid Base DN is required")
	}
	if strings.TrimSpace(cfg.BindDN) == "" || hasControl(cfg.BindDN) || strings.TrimSpace(cfg.BindPassword) == "" {
		return "", nil, invalidConfig("query account and password are required")
	}
	if len(cfg.AllowedGroupDNs) == 0 {
		return "", nil, invalidConfig("at least one allowed group DN is required")
	}
	for _, group := range cfg.AllowedGroupDNs {
		dn, parseErr := ldap.ParseDN(group)
		if parseErr != nil || len(dn.RDNs) == 0 {
			return "", nil, invalidConfig("invalid allowed group DN")
		}
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if strings.TrimSpace(cfg.CAPEM) != "" {
		data := []byte(cfg.CAPEM)
		count := 0
		for len(strings.TrimSpace(string(data))) > 0 {
			data = []byte(strings.TrimSpace(string(data)))
			if !strings.HasPrefix(string(data), "-----BEGIN CERTIFICATE-----") {
				return "", nil, invalidConfig("invalid CA PEM certificate")
			}
			block, rest := pem.Decode(data)
			if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
				return "", nil, invalidConfig("invalid CA PEM certificate")
			}
			certificate, parseErr := x509.ParseCertificate(block.Bytes)
			if parseErr != nil || !certificate.IsCA {
				return "", nil, invalidConfig("CA PEM must contain CA certificates")
			}
			roots.AddCert(certificate)
			count++
			data = rest
		}
		if count == 0 {
			return "", nil, invalidConfig("invalid CA PEM certificate")
		}
	}
	return net.JoinHostPort(u.Hostname(), port), &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: u.Hostname(),
		RootCAs:    roots,
	}, nil
}

func invalidConfig(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, reason)
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func normalizeUsername(username string) (string, error) {
	if hasControl(username) {
		return "", ErrInvalidCredentials
	}
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 256 || strings.ContainsAny(username, "\\@") || hasControl(username) {
		return "", ErrInvalidCredentials
	}
	return username, nil
}

// directoryFailure deliberately does not expose directory diagnostics, DNs,
// passwords, or TLS errors returned by an external server.
func directoryFailure(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, stage, err)
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, stage)
}

func connect(ctx context.Context, cfg Config) (*ldap.Conn, func(), error) {
	addr, tlsConfig, err := connectionSettings(cfg)
	if err != nil {
		return nil, nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: tlsConfig}
	netConn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, nil, directoryFailure(dialCtx, "TLS connection")
	}
	conn := ldap.NewConn(netConn, true)
	conn.SetTimeout(operationTimeout)
	conn.Start()
	stop := context.AfterFunc(ctx, func() { _ = netConn.Close() })
	closeConn := func() {
		stop()
		_ = conn.Close()
	}
	return conn, closeConn, nil
}

func queryConnection(ctx context.Context, cfg Config) (*ldap.Conn, func(), error) {
	conn, closeConn, err := connect(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := conn.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
		closeConn()
		return nil, nil, directoryFailure(ctx, "query account bind")
	}
	return conn, closeConn, nil
}

func userFilter(username string, groups []string) string {
	var allowed strings.Builder
	allowed.WriteString("(|")
	for _, group := range groups {
		fmt.Fprintf(&allowed, "(memberOf:%s:=%s)", memberOfChain, ldap.EscapeFilter(group))
	}
	allowed.WriteByte(')')
	return fmt.Sprintf("(&(objectCategory=person)(objectClass=user)(sAMAccountName=%s)(!(userAccountControl:1.2.840.113556.1.4.803:=2))%s)", ldap.EscapeFilter(username), allowed.String())
}

func search(conn *ldap.Conn, base string, scope int, filter string, attributes []string) (*ldap.SearchResult, error) {
	return conn.Search(ldap.NewSearchRequest(base, scope, ldap.NeverDerefAliases, 2, 5, false, filter, attributes, nil))
}

func checkedSingle(ctx context.Context, conn *ldap.Conn, dn, filter, stage string, attributes []string) (*ldap.Entry, error) {
	result, err := search(conn, dn, ldap.ScopeBaseObject, filter, attributes)
	if err != nil || result == nil || len(result.Referrals) != 0 || len(result.Entries) != 1 {
		return nil, directoryFailure(ctx, stage)
	}
	want, wantErr := ldap.ParseDN(dn)
	actual, actualErr := ldap.ParseDN(result.Entries[0].DN)
	if wantErr != nil || actualErr != nil || !want.EqualFold(actual) {
		return nil, directoryFailure(ctx, stage)
	}
	return result.Entries[0], nil
}

func guid(entry *ldap.Entry) (string, bool) {
	values := entry.GetEqualFoldRawAttributeValues("objectGUID")
	if len(values) != 1 || len(values[0]) != 16 {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(values[0]), true
}

func (c *Client) Authenticate(ctx context.Context, cfg Config, username, password string) (*Profile, error) {
	username, err := normalizeUsername(username)
	if err != nil || strings.TrimSpace(password) == "" {
		return nil, ErrInvalidCredentials
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	conn, closeConn, err := queryConnection(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	result, err := search(conn, cfg.BaseDN, ldap.ScopeWholeSubtree, userFilter(username, cfg.AllowedGroupDNs), []string{"objectGUID", "sAMAccountName", "displayName", "mail", "userAccountControl"})
	if ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, ErrInvalidCredentials
	}
	if err != nil || result == nil || len(result.Referrals) != 0 {
		return nil, directoryFailure(ctx, "user search")
	}
	if len(result.Entries) != 1 {
		return nil, ErrInvalidCredentials
	}
	entry := result.Entries[0]
	userGUID, valid := guid(entry)
	loginName, loginErr := normalizeUsername(entry.GetEqualFoldAttributeValue("sAMAccountName"))
	dn, dnErr := ldap.ParseDN(entry.DN)
	base, _ := ldap.ParseDN(cfg.BaseDN)
	if !valid || loginErr != nil || !strings.EqualFold(loginName, username) || dnErr != nil || len(dn.RDNs) == 0 || !base.AncestorOfFold(dn) {
		return nil, directoryFailure(ctx, "user attributes")
	}
	uac, uacErr := strconv.ParseUint(entry.GetEqualFoldAttributeValue("userAccountControl"), 10, 32)
	if uacErr != nil {
		return nil, directoryFailure(ctx, "user attributes")
	}
	if uac&2 != 0 {
		return nil, ErrInvalidCredentials
	}
	userConn, closeUser, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	bindErr := userConn.Bind(entry.DN, password)
	closeUser()
	if bindErr != nil {
		if ldap.IsErrorWithCode(bindErr, ldap.LDAPResultInvalidCredentials) || ldap.IsErrorWithCode(bindErr, ldap.LDAPResultInappropriateAuthentication) {
			return nil, ErrInvalidCredentials
		}
		return nil, directoryFailure(ctx, "user bind")
	}
	department, err := departmentFromDN(ctx, conn, dn)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(entry.GetEqualFoldAttributeValue("displayName"))
	if displayName == "" {
		displayName = loginName
	}
	return &Profile{
		GUID: userGUID, Username: loginName, DisplayName: displayName,
		Email: strings.TrimSpace(entry.GetEqualFoldAttributeValue("mail")), Department: department,
	}, nil
}

func departmentFromDN(ctx context.Context, conn *ldap.Conn, dn *ldap.DN) (Department, error) {
	nearest := -1
	var parts []string
	for i := 1; i < len(dn.RDNs); i++ {
		for _, attr := range dn.RDNs[i].Attributes {
			if strings.EqualFold(attr.Type, "OU") {
				if nearest == -1 {
					nearest = i
				}
				parts = append(parts, attr.Value)
			}
		}
	}
	if nearest == -1 {
		return Department{GUID: "unassigned", Path: "未分配部门"}, nil
	}
	ouDN := (&ldap.DN{RDNs: dn.RDNs[nearest:]}).String()
	entry, err := checkedSingle(ctx, conn, ouDN, "(objectClass=organizationalUnit)", "department search", []string{"objectGUID"})
	if err != nil {
		return Department{}, err
	}
	ouGUID, valid := guid(entry)
	if !valid {
		return Department{}, directoryFailure(ctx, "department attributes")
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return Department{GUID: ouGUID, DN: ouDN, Path: strings.Join(parts, "／")}, nil
}

func (c *Client) Test(ctx context.Context, cfg Config) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	conn, closeConn, err := queryConnection(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeConn()
	if _, err := checkedSingle(ctx, conn, cfg.BaseDN, "(objectClass=*)", "base DN search", []string{"objectClass"}); err != nil {
		return err
	}
	for _, dn := range cfg.AllowedGroupDNs {
		if _, err := checkedSingle(ctx, conn, dn, "(objectClass=group)", "allowed group search", []string{"objectClass"}); err != nil {
			return err
		}
	}
	return nil
}
