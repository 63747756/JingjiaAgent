package adldap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
)

const (
	baseDN   = "DC=test,DC=local"
	readerDN = "CN=Reader," + baseDN
	groupDN  = "CN=Allowed," + baseDN
	userDN   = "CN=Employee,OU=Soft\\,ware,OU=Research,OU=Company," + baseDN
)

var userGUID = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
var ouGUID = []byte{15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}

type ldapRequest struct {
	id int64
	op *ber.Packet
}

type wireHandler func(ldapRequest) []*ber.Packet

func envelope(id int64, op *ber.Packet) *ber.Packet {
	p := ber.NewSequence("LDAP message")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "message ID"))
	p.AppendChild(op)
	return p
}

func result(id int64, tag ber.Tag, code uint16, diagnostic string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "LDAP result")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "result code"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "matched DN"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, diagnostic, "diagnostic"))
	return envelope(id, op)
}

func entryPacket(id int64, dn string, attributes map[string][]string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, ldap.ApplicationSearchResultEntry, nil, "search entry")
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, dn, "entry DN"))
	attrs := ber.NewSequence("attributes")
	for name, values := range attributes {
		attr := ber.NewSequence("attribute")
		attr.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, name, "type"))
		set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "values")
		for _, value := range values {
			set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, value, "value"))
		}
		attr.AppendChild(set)
		attrs.AppendChild(attr)
	}
	op.AppendChild(attrs)
	return envelope(id, op)
}

func testCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "AD unit-test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "AD unit-test server"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{serverDER, caDER}, PrivateKey: serverKey}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

// newWireDirectory speaks real BER over a verified TLS socket. It is deliberately
// in a _test.go file and cannot be selected by a production configuration.
func newWireDirectory(t *testing.T, handler wireHandler) Config {
	t.Helper()
	certificate, caPEM := testCertificate(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var connections []net.Conn
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				for {
					_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
					packet, err := ber.ReadPacket(conn)
					if err != nil || len(packet.Children) < 2 {
						return
					}
					request := ldapRequest{id: packet.Children[0].Value.(int64), op: packet.Children[1]}
					for _, response := range handler(request) {
						if _, err := conn.Write(response.Bytes()); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return Config{URL: "ldaps://" + listener.Addr().String(), BaseDN: baseDN, BindDN: readerDN, BindPassword: "query secret", CAPEM: caPEM, AllowedGroupDNs: []string{groupDN}}
}

func normalDirectory(request ldapRequest) []*ber.Packet {
	if request.op.Tag == ldap.ApplicationBindRequest {
		name := request.op.Children[1].Value.(string)
		password := request.op.Children[2].Data.String()
		if (name == readerDN && password == "query secret") || (name == userDN && password == "  user secret  ") || (name == "CN=Employee,CN=Users,"+baseDN && password == "  user secret  ") {
			return []*ber.Packet{result(request.id, ldap.ApplicationBindResponse, ldap.LDAPResultSuccess, "")}
		}
		return []*ber.Packet{result(request.id, ldap.ApplicationBindResponse, ldap.LDAPResultInvalidCredentials, "secret text must not escape")}
	}
	if request.op.Tag != ldap.ApplicationSearchRequest {
		return nil
	}
	base := request.op.Children[0].Value.(string)
	filter, _ := ldap.DecompileFilter(request.op.Children[6])
	var entry *ber.Packet
	switch {
	case strings.Contains(filter, "sAMAccountName="):
		entry = entryPacket(request.id, userDN, map[string][]string{
			"OBJECTGUID": {string(userGUID)}, "sAMAccountName": {"employee"},
			"displayName": {"Employee Name"}, "mail": {"employee@test.local"}, "userAccountControl": {"512"},
		})
	case strings.EqualFold(base, baseDN):
		entry = entryPacket(request.id, baseDN, map[string][]string{"objectClass": {"domain"}})
	case strings.EqualFold(base, groupDN):
		entry = entryPacket(request.id, groupDN, map[string][]string{"objectClass": {"group"}})
	default:
		entry = entryPacket(request.id, base, map[string][]string{"objectGUID": {string(ouGUID)}})
	}
	return []*ber.Packet{entry, result(request.id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess, "")}
}

func TestAuthenticateVerifiedLDAPSAndDepartment(t *testing.T) {
	var filter string
	var mu sync.Mutex
	cfg := newWireDirectory(t, func(r ldapRequest) []*ber.Packet {
		if r.op.Tag == ldap.ApplicationSearchRequest {
			f, _ := ldap.DecompileFilter(r.op.Children[6])
			if strings.Contains(f, "sAMAccountName=") {
				mu.Lock()
				filter = f
				mu.Unlock()
				if r.op.Children[1].Value.(int64) != ldap.ScopeWholeSubtree {
					t.Error("user query must use subtree scope")
				}
			}
		}
		return normalDirectory(r)
	})
	cfg.AllowedGroupDNs = append(cfg.AllowedGroupDNs, "CN=Other\\,group,"+baseDN)
	profile, err := New().Authenticate(context.Background(), cfg, " employee ", "  user secret  ")
	if err != nil {
		t.Fatal(err)
	}
	if profile.GUID != base64.RawURLEncoding.EncodeToString(userGUID) || profile.Department.GUID != base64.RawURLEncoding.EncodeToString(ouGUID) {
		t.Fatalf("incorrect raw GUID mapping: %+v", profile)
	}
	if profile.Username != "employee" || profile.DisplayName != "Employee Name" || profile.Email != "employee@test.local" {
		t.Fatalf("incorrect profile: %+v", profile)
	}
	if profile.Department.Path != "Company／Research／Soft,ware" || !strings.Contains(profile.Department.DN, "Soft\\,ware") {
		t.Fatalf("escaped OU not parsed: %+v", profile.Department)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(filter, "(memberOf:"+memberOfChain+":="+groupDN+")") || !strings.Contains(filter, "(!(userAccountControl:1.2.840.113556.1.4.803:=2))") || !strings.Contains(filter, "(|") {
		t.Fatalf("missing AD authorization constraints: %s", filter)
	}
}

func TestUserFilterEscapesUntrustedValues(t *testing.T) {
	username := "a*)(sAMAccountName=*)"
	group := "CN=Allowed(*)," + baseDN
	filter := userFilter(username, []string{group})
	if !strings.Contains(filter, "sAMAccountName="+ldap.EscapeFilter(username)) || !strings.Contains(filter, ldap.EscapeFilter(group)) {
		t.Fatal("filter values were not escaped")
	}
	if _, err := ldap.CompileFilter(filter); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfig(t *testing.T) {
	valid := Config{URL: "ldaps://dc.test.local", BaseDN: baseDN, BindDN: readerDN, BindPassword: "secret", AllowedGroupDNs: []string{groupDN}}
	addr, settings, err := connectionSettings(valid)
	if err != nil || addr != "dc.test.local:636" || settings.InsecureSkipVerify || settings.MinVersion != tls.VersionTLS12 {
		t.Fatalf("incorrect secure defaults: %s %+v %v", addr, settings, err)
	}
	tests := map[string]func(*Config){
		"cleartext":         func(c *Config) { c.URL = "ldap://dc.test.local" },
		"starttls":          func(c *Config) { c.URL = "ldap://dc.test.local:389" },
		"URL credentials":   func(c *Config) { c.URL = "ldaps://reader:secret@dc.test.local" },
		"URL path":          func(c *Config) { c.URL = "ldaps://dc.test.local/" },
		"URL query":         func(c *Config) { c.URL = "ldaps://dc.test.local?skip_verify=true" },
		"bad port":          func(c *Config) { c.URL = "ldaps://dc.test.local:65536" },
		"no base":           func(c *Config) { c.BaseDN = "" },
		"invalid base":      func(c *Config) { c.BaseDN = "not a DN" },
		"no query account":  func(c *Config) { c.BindDN = "" },
		"no query password": func(c *Config) { c.BindPassword = " " },
		"no allowed groups": func(c *Config) { c.AllowedGroupDNs = nil },
		"bad group":         func(c *Config) { c.AllowedGroupDNs = []string{"group"} },
		"bad CA":            func(c *Config) { c.CAPEM = "not a certificate" },
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			c := valid
			edit(&c)
			if err := ValidateConfig(c); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestShortUsernameAndBlankPasswordRejectedWithoutNetwork(t *testing.T) {
	for _, username := range []string{"", " ", "DOMAIN\\employee", "employee@test.local", "em\x00ployee", "em\nployee", "\nemployee", "employee\t"} {
		if _, err := New().Authenticate(context.Background(), Config{}, username, "secret"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("username %q got %v", username, err)
		}
	}
	for _, password := range []string{"", "\t \r\n"} {
		if _, err := New().Authenticate(context.Background(), Config{}, "employee", password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("blank password got %v", err)
		}
	}
}

func TestAuthenticateFailureSemantics(t *testing.T) {
	cases := []struct {
		name   string
		change func(ldapRequest, []*ber.Packet) []*ber.Packet
		want   error
	}{
		{"no account or group excluded", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && strings.Contains(r.op.Children[0].Value.(string), "DC=") && r.op.Children[1].Value.(int64) == ldap.ScopeWholeSubtree {
				return p[1:]
			}
			return p
		}, ErrInvalidCredentials},
		{"ambiguous account", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && r.op.Children[1].Value.(int64) == ldap.ScopeWholeSubtree {
				return []*ber.Packet{p[0], p[0], p[1]}
			}
			return p
		}, ErrInvalidCredentials},
		{"bad GUID", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && r.op.Children[1].Value.(int64) == ldap.ScopeWholeSubtree {
				p[0] = entryPacket(r.id, userDN, map[string][]string{"objectGUID": {"text-not-raw-guid"}, "sAMAccountName": {"employee"}, "userAccountControl": {"512"}})
			}
			return p
		}, ErrUnavailable},
		{"disabled account", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && r.op.Children[1].Value.(int64) == ldap.ScopeWholeSubtree {
				p[0] = entryPacket(r.id, userDN, map[string][]string{"objectGUID": {string(userGUID)}, "sAMAccountName": {"employee"}, "userAccountControl": {"514"}})
			}
			return p
		}, ErrInvalidCredentials},
		{"OU missing", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && strings.HasPrefix(strings.ToUpper(r.op.Children[0].Value.(string)), "OU=") {
				return p[1:]
			}
			return p
		}, ErrUnavailable},
		{"OU GUID missing", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && strings.HasPrefix(strings.ToUpper(r.op.Children[0].Value.(string)), "OU=") {
				p[0] = entryPacket(r.id, r.op.Children[0].Value.(string), nil)
			}
			return p
		}, ErrUnavailable},
		{"query account failed", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationBindRequest && r.op.Children[1].Value.(string) == readerDN {
				return []*ber.Packet{result(r.id, ldap.ApplicationBindResponse, ldap.LDAPResultInvalidCredentials, "query secret user secret")}
			}
			return p
		}, ErrUnavailable},
		{"OU permission denied", func(r ldapRequest, p []*ber.Packet) []*ber.Packet {
			if r.op.Tag == ldap.ApplicationSearchRequest && strings.HasPrefix(strings.ToUpper(r.op.Children[0].Value.(string)), "OU=") {
				return []*ber.Packet{result(r.id, ldap.ApplicationSearchResultDone, ldap.LDAPResultInsufficientAccessRights, "query secret user secret")}
			}
			return p
		}, ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newWireDirectory(t, func(r ldapRequest) []*ber.Packet { return tc.change(r, normalDirectory(r)) })
			profile, err := New().Authenticate(context.Background(), cfg, "employee", "  user secret  ")
			if profile != nil || !errors.Is(err, tc.want) {
				t.Fatalf("profile=%+v err=%v", profile, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("server diagnostics leaked")
			}
		})
	}
}

func TestWrongUserPasswordIsNotDirectoryOutage(t *testing.T) {
	cfg := newWireDirectory(t, normalDirectory)
	_, err := New().Authenticate(context.Background(), cfg, "employee", "wrong password")
	if !errors.Is(err, ErrInvalidCredentials) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestNoOUIsUnassignedAndEmailMayBeEmpty(t *testing.T) {
	cfg := newWireDirectory(t, func(r ldapRequest) []*ber.Packet {
		p := normalDirectory(r)
		if r.op.Tag == ldap.ApplicationSearchRequest && r.op.Children[1].Value.(int64) == ldap.ScopeWholeSubtree {
			p[0] = entryPacket(r.id, "CN=Employee,CN=Users,"+baseDN, map[string][]string{"objectGUID": {string(userGUID)}, "sAMAccountName": {"employee"}, "userAccountControl": {"512"}})
		}
		return p
	})
	profile, err := New().Authenticate(context.Background(), cfg, "employee", "  user secret  ")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Department.GUID != "unassigned" || profile.Department.Path != "未分配部门" || profile.Department.DN != "" || profile.Email != "" || profile.DisplayName != "employee" {
		t.Fatalf("got %+v", profile)
	}
}

func TestDirectoryConnectionTest(t *testing.T) {
	t.Run("valid base and groups", func(t *testing.T) {
		cfg := newWireDirectory(t, normalDirectory)
		if err := New().Test(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	})
	for _, missing := range []string{baseDN, groupDN} {
		t.Run("missing "+missing, func(t *testing.T) {
			cfg := newWireDirectory(t, func(r ldapRequest) []*ber.Packet {
				p := normalDirectory(r)
				if r.op.Tag == ldap.ApplicationSearchRequest && r.op.Children[0].Value.(string) == missing {
					return p[1:]
				}
				return p
			})
			if err := New().Test(context.Background(), cfg); !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}

func TestTLSCertificateVerificationCannotBeDisabled(t *testing.T) {
	t.Run("untrusted CA", func(t *testing.T) {
		cfg := newWireDirectory(t, normalDirectory)
		cfg.CAPEM = ""
		if err := New().Test(context.Background(), cfg); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})
	t.Run("hostname mismatch", func(t *testing.T) {
		cfg := newWireDirectory(t, normalDirectory)
		cfg.URL = strings.Replace(cfg.URL, "127.0.0.1", "localhost", 1)
		if err := New().Test(context.Background(), cfg); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})
}

func TestContextCancellationInterruptsLDAPAndTLS(t *testing.T) {
	t.Run("LDAP bind", func(t *testing.T) {
		cfg := newWireDirectory(t, func(r ldapRequest) []*ber.Packet { return nil })
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := New().Test(ctx, cfg)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) || time.Since(start) > time.Second {
			t.Fatalf("cancellation not respected: %v elapsed=%v", err, time.Since(start))
		}
	})
	t.Run("TLS handshake", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		accepted := make(chan net.Conn, 1)
		go func() { conn, _ := listener.Accept(); accepted <- conn }()
		cfg := Config{URL: "ldaps://" + listener.Addr().String(), BaseDN: baseDN, BindDN: readerDN, BindPassword: "secret", AllowedGroupDNs: []string{groupDN}}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		start := time.Now()
		err = New().Test(ctx, cfg)
		conn := <-accepted
		if conn != nil {
			defer conn.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("TLS cancellation not respected: %v elapsed=%v", err, time.Since(start))
		}
	})
}

func TestReferralsAreRejected(t *testing.T) {
	cfg := newWireDirectory(t, func(r ldapRequest) []*ber.Packet {
		p := normalDirectory(r)
		if r.op.Tag == ldap.ApplicationSearchRequest {
			ref := ber.Encode(ber.ClassApplication, ber.TypeConstructed, ldap.ApplicationSearchResultReference, nil, "referral")
			ref.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "ldaps://untrusted.test", "URL"))
			p = append([]*ber.Packet{envelope(r.id, ref)}, p...)
		}
		return p
	})
	if _, err := New().Authenticate(context.Background(), cfg, "employee", "  user secret  "); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
