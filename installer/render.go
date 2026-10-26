package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type deployment struct {
	Sequence                                                                      int64
	Schema                                                                        int `json:"schema"`
	Root, Data, PublicURL, InternalURL, PreviewURL, Mode, Project, Version, Phase string
	PreviewMode, PreviewPortRange, PreviewSubnet                                  string
	PreviewSeparateTLS                                                            bool
	TrustedProxyCIDRs                                                             []string
	DatabaseGeneration                                                            int
	Images                                                                        map[string]imageRecord
}

func privateWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if s, err := os.Lstat(path); err == nil && (!s.Mode().IsRegular() || s.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing non-regular private state")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return privateWrite(path, append(b, '\n'))
}
func readState(root string) (*deployment, error) {
	b, e := os.ReadFile(filepath.Join(root, "deployment.json"))
	if e != nil {
		return nil, e
	}
	var s deployment
	e = json.Unmarshal(b, &s)
	if e != nil {
		return nil, e
	}
	if s.Schema != 1 || s.Root != root || !filepath.IsAbs(s.Data) || !safeName.MatchString(s.Project) {
		return nil, errors.New("invalid installation state")
	}
	if err := s.normalizeAddresses(); err != nil {
		return nil, err
	}
	return &s, nil
}
func saveState(s *deployment) error { return writeJSON(filepath.Join(s.Root, "deployment.json"), s) }
func secret(root, name string, bytes int) (string, error) {
	path := filepath.Join(root, "secrets", name)
	b, e := os.ReadFile(path)
	if e == nil {
		if len(b) != bytes {
			return "", errors.New("invalid existing secret: " + name)
		}
		return string(b), nil
	}
	if !os.IsNotExist(e) {
		return "", e
	}
	b = make([]byte, bytes)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	return string(b), privateWrite(path, b)
}
func token(root, name string) (string, error) {
	path := filepath.Join(root, "secrets", name)
	b, e := os.ReadFile(path)
	if e == nil {
		if len(b) != 64 {
			return "", errors.New("invalid token: " + name)
		}
		return string(b), nil
	}
	if !os.IsNotExist(e) {
		return "", e
	}
	b = make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	value := hex.EncodeToString(b)
	return value, privateWrite(path, []byte(value))
}

func generateTLS(s *deployment, o options) error {
	certPath, keyPath := filepath.Join(s.Root, "tls/server.crt"), filepath.Join(s.Root, "tls/server.key")
	u, _ := url.Parse(s.internalURL())
	key, e := rsa.GenerateKey(rand.Reader, 3072)
	if e != nil {
		return e
	}
	now := time.Now()
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "JingjiaAgent installation CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caBytes, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		return e
	}
	// Parse the generated SubjectKeyId so issued leaves include AuthorityKeyId
	// (required by strict OpenSSL clients, including current Python).
	ca, e = x509.ParseCertificate(caBytes)
	if e != nil {
		return e
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes})
	if e = privateWrite(filepath.Join(s.Root, "tls/ca.key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})); e != nil {
		return e
	}
	for _, name := range []string{"server", "runtime"} {
		serverKey, e := rsa.GenerateKey(rand.Reader, 3072)
		if e != nil {
			return e
		}
		serial, _ = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: u.Hostname()}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if name == "runtime" {
			cert.DNSNames = []string{"runtime-proxy"}
		} else {
			setServerNames(cert, s)
		}
		der, e := x509.CreateCertificate(rand.Reader, cert, ca, &serverKey.PublicKey, key)
		if e != nil {
			return e
		}
		if e = privateWrite(filepath.Join(s.Root, "tls", name+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); e != nil {
			return e
		}
		if e = privateWrite(filepath.Join(s.Root, "tls", name+".key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})); e != nil {
			return e
		}
	}
	if o.Certificate != "" || o.Key != "" {
		if o.Certificate == "" || o.Key == "" {
			return errors.New("--tls-cert and --tls-key must be supplied together")
		}
		for dst, src := range map[string]string{certPath: o.Certificate, keyPath: o.Key} {
			b, e := os.ReadFile(src)
			if e != nil {
				return e
			}
			if e = privateWrite(dst, b); e != nil {
				return e
			}
		}
	}
	if o.CA != "" {
		b, e := os.ReadFile(o.CA)
		if e != nil {
			return e
		}
		caPEM = append(caPEM, b...)
	}
	if e = copyPreviewTLS(s, o); e != nil {
		return e
	}
	if e = privateWrite(filepath.Join(s.Root, "tls/ca.crt"), caPEM); e != nil {
		return e
	}
	bundle := []byte{}
	for _, p := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt"} {
		if b, e := os.ReadFile(p); e == nil {
			bundle = b
			break
		}
	}
	bundle = append(bundle, caPEM...)
	if e = privateWrite(filepath.Join(s.Root, "tls/ca-bundle.crt"), bundle); e != nil {
		return e
	}
	return validateServerTLS(s, certPath, keyPath)
}

func validateTLS(raw, certPath, keyPath string) error {
	cert, e := os.ReadFile(certPath)
	if e != nil {
		return e
	}
	key, e := os.ReadFile(keyPath)
	if e != nil {
		return e
	}
	if len(key) == 0 {
		return errors.New("empty TLS key")
	}
	if _, e = tls.X509KeyPair(cert, key); e != nil {
		return e
	}
	b, _ := pem.Decode(cert)
	if b == nil {
		return errors.New("invalid TLS certificate")
	}
	c, e := x509.ParseCertificate(b.Bytes)
	if e != nil {
		return e
	}
	u, _ := url.Parse(raw)
	if e = c.VerifyHostname(u.Hostname()); e != nil {
		return e
	}
	if time.Now().After(c.NotAfter) {
		return errors.New("TLS certificate expired")
	}
	if time.Now().Before(c.NotBefore) {
		return errors.New("TLS certificate is not yet valid")
	}
	return nil
}

type object = map[string]any

func render(s *deployment) error {
	if err := s.normalizeAddresses(); err != nil {
		return err
	}
	if s.PreviewSubnet == "" {
		return errors.New("缺少受信任的网关网络，请先分配预览入口网络")
	}
	gatewayIP, backendIP, err := previewNetworkAddresses(s.PreviewSubnet)
	if err != nil {
		return err
	}
	// Existing encrypted data must never silently receive replacement keys.
	if _, err := os.Stat(filepath.Join(s.Root, "config/server/config.yaml")); err == nil {
		if err = checkSecrets(s); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Nested read-only mounts need an existing mountpoint in the parent bind.
	if err := os.MkdirAll(filepath.Join(s.Root, "config/tls"), 0700); err != nil {
		return err
	}
	pg, e := token(s.Root, "postgres.password")
	if e != nil {
		return e
	}
	redis, e := token(s.Root, "redis.password")
	if e != nil {
		return e
	}
	storage, e := token(s.Root, "storage.password")
	if e != nil {
		return e
	}
	node, e := token(s.Root, "node.token")
	if e != nil {
		return e
	}
	_, e = token(s.Root, "converter.token")
	if e != nil {
		return e
	}
	mcp, e := token(s.Root, "mcp.token")
	if e != nil {
		return e
	}
	if _, e = token(s.Root, "setup.token"); e != nil {
		return e
	}
	for _, name := range []string{"ad.key", "payload.key"} {
		if _, e = secret(s.Root, name, 32); e != nil {
			return e
		}
	}
	img := func(name string) string { return s.Images[name].ID }
	cfg := object{
		"debug": false, "logger": object{"level": "info"}, "server": object{"addr": "0.0.0.0:8888", "base_url": s.PublicURL}, "root_path": "/app/data",
		"database": object{"master": "postgres://postgres:" + pg + "@postgres:5432/jingjiaagent?sslmode=disable"}, "redis": object{"host": "redis", "port": 6379, "pass": redis},
		"ad": object{"secret_key_file": "/run/secrets/ad-secret-key"}, "security": object{"captcha_enabled": false},
		"deployment": object{"enabled": true, "setup_token_file": "/run/secrets/setup.token", "guest_image": img("guest"), "local_node": s.Mode == "single", "local_node_url": "https://runtime-proxy:7411", "local_node_ca_file": "/app/config/tls/ca.crt", "local_node_token_file": "/run/secrets/node.token"},
		"runtime":    object{"backend": "agent_compose", "deployment_managed": true, "payload_key_file": "/run/secrets/payload.key", "poll_interval": "1s", "callback_base_url": "http://127.0.0.1:8888", "mcp_url": s.PublicURL + "/mcp", "platform_ca_file": "/app/config/tls/ca.crt", "installer_manifest_file": "/app/release/node/manifest.json", "installer_base_url": s.PublicURL, "installer_binary_file": "/app/release/jingjiaagent", "capacity": object{"enabled": true, "reserve_cpu_millis": 1000, "reserve_memory_bytes": int64(2 << 30)}},
		"llm_proxy":  object{"base_url": s.PublicURL}, "mcp_hub": object{"enabled": true, "url": "http://127.0.0.1:8888", "token": mcp}, "taskflow": object{"callback_token": node},
		"clickhouse":     object{"addr": "clickhouse:9000", "database": "jingjiaagent", "username": "jingjiaagent", "password": pg, "init_enabled": true},
		"work":           object{"enabled": true, "converter_url": "http://converter:8090", "converter_token_file": "/run/secrets/converter.token"},
		"object_storage": object{"enabled": true, "provider": "s3", "force_path_style": true, "init_bucket": true, "endpoint": "http://storage:9000", "access_endpoint": s.PublicURL + "/oss", "agent_access_endpoint": s.PublicURL + "/oss", "access_key": "jingjiaagent", "access_key_secret": storage, "bucket": "jingjiaagent", "region": "us-east-1", "presign_expires": "1h", "temp_prefix": "temp", "avatar_prefix": "avatar", "spec_prefix": "spec", "repo_prefix": "repo"},
	}
	configPath := filepath.Join(s.Root, "config/server/config.yaml")
	if previous, err := os.ReadFile(configPath); err == nil {
		var existing object
		if err = json.Unmarshal(previous, &existing); err != nil {
			return errors.New("existing managed JSON configuration is invalid")
		}
		for key, value := range existing {
			if key != "deployment" {
				cfg[key] = value
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// These origins belong to the deployment, not editable provider settings.
	// Keep employee URLs separate from all node/workspace credential traffic.
	for section, values := range map[string]object{
		"server":         {"base_url": s.PublicURL},
		"runtime":        {"mcp_url": s.internalURL() + "/mcp", "installer_base_url": s.internalURL()},
		"llm_proxy":      {"base_url": s.internalURL()},
		"object_storage": {"access_endpoint": s.PublicURL + "/oss", "agent_access_endpoint": s.internalURL() + "/oss"},
	} {
		part, ok := cfg[section].(object)
		if !ok {
			return fmt.Errorf("invalid managed configuration section: %s", section)
		}
		for key, value := range values {
			part[key] = value
		}
	}
	cfg["runtime"].(object)["preview"] = object{"mode": s.PreviewMode, "base_url": s.PreviewURL, "port_range": s.PreviewPortRange, "listen": "0.0.0.0:8889", "trusted_proxies": []string{gatewayIP + "/32"}}
	if e = writeJSON(configPath, cfg); e != nil {
		return e
	}
	service := func(name string, networks ...string) object {
		return object{"image": img(name), "pull_policy": "never", "restart": "unless-stopped", "networks": networks, "logging": object{"driver": "json-file", "options": object{"max-size": "20m", "max-file": "3"}}}
	}
	dataMount := func(name, target string) []string {
		return []string{filepath.ToSlash(filepath.Join(s.Data, name)) + ":" + target}
	}
	pgService := service("postgres", "business")
	pgService["environment"] = object{"POSTGRES_DB": "jingjiaagent", "POSTGRES_PASSWORD": pg}
	pgService["volumes"] = dataMount("postgres", "/var/lib/postgresql/data")
	pgService["healthcheck"] = health("pg_isready -U postgres -d jingjiaagent")
	rd := service("redis", "business")
	rd["command"] = []string{"sh", "-ec", "exec redis-server --appendonly yes --requirepass \"$(cat /run/secrets/redis.password)\""}
	rd["volumes"] = append(dataMount("redis", "/data"), filepath.ToSlash(filepath.Join(s.Root, "secrets/redis.password"))+":/run/secrets/redis.password:ro")
	st := service("storage", "business")
	st["environment"] = object{"MINIO_ROOT_USER": "jingjiaagent", "MINIO_ROOT_PASSWORD": storage}
	st["command"] = []string{"server", "/data"}
	st["volumes"] = dataMount("storage", "/data")
	st["healthcheck"] = health("curl -fsS http://127.0.0.1:9000/minio/health/ready")
	ch := service("clickhouse", "business")
	ch["environment"] = object{"CLICKHOUSE_DB": "jingjiaagent", "CLICKHOUSE_USER": "jingjiaagent", "CLICKHOUSE_PASSWORD": pg, "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1"}
	ch["volumes"] = dataMount("clickhouse", "/var/lib/clickhouse")
	ch["healthcheck"] = health("wget -qO- http://127.0.0.1:8123/ping")
	ch["ulimits"] = object{"nofile": object{"soft": 262144, "hard": 262144}}
	// The API and task network use normal host routing. Data services stay internal.
	be := service("backend", "business", "sandbox", "conversion", "management")
	be["networks"] = object{"business": object{}, "sandbox": object{}, "conversion": object{}, "management": object{}, "preview_ingress": object{"ipv4_address": backendIP, "aliases": []string{"backend-preview"}}}
	be["environment"] = object{"SSL_CERT_FILE": "/app/config/tls/ca-bundle.crt"}
	be["volumes"] = append(dataMount("backend", "/app/data"), filepath.ToSlash(filepath.Join(s.Root, "config"))+":/app/config:ro", filepath.ToSlash(filepath.Join(s.Root, "tls"))+":/app/config/tls:ro", filepath.ToSlash(filepath.Join(s.Root, "secrets"))+":/run/secrets:ro", filepath.ToSlash(filepath.Join(s.Root, "release"))+":/app/release:ro")
	// The entrypoint copies AD material to a writable /run/secrets target.
	be["volumes"] = append(dataMount("backend", "/app/data"), filepath.ToSlash(filepath.Join(s.Root, "config"))+":/app/config:ro", filepath.ToSlash(filepath.Join(s.Root, "tls"))+":/app/config/tls:ro", filepath.ToSlash(filepath.Join(s.Root, "release"))+":/app/release:ro")
	for _, name := range []string{"ad.key", "payload.key", "node.token", "converter.token", "setup.token"} {
		target := name
		if name == "ad.key" {
			target = "ad-secret-key-source"
		}
		be["volumes"] = append(be["volumes"].([]string), filepath.ToSlash(filepath.Join(s.Root, "secrets", name))+":/run/secrets/"+target+":ro")
	}
	be["depends_on"] = object{"postgres": object{"condition": "service_healthy"}, "storage": object{"condition": "service_healthy"}, "clickhouse": object{"condition": "service_healthy"}, "redis": object{"condition": "service_started"}}
	be["healthcheck"] = health("curl -fsS http://127.0.0.1:8888/api/v1/deployment/status")
	web := service("frontend", "business")
	web["volumes"] = []string{filepath.ToSlash(filepath.Join(s.Root, "web.conf")) + ":/etc/nginx/conf.d/default.conf:ro"}
	conv := service("converter", "conversion")
	conv["user"] = "0:0" // Read the root-owned token, then converter drops to UID 10001.
	conv["healthcheck"] = health("/opt/office/venv/bin/python -c \"import urllib.request;urllib.request.urlopen('http://127.0.0.1:8090/health',timeout=3)\"")
	conv["read_only"] = true
	conv["cap_drop"] = []string{"ALL"}
	conv["cap_add"] = []string{"SETUID", "SETGID", "DAC_OVERRIDE", "KILL"}
	conv["security_opt"] = []string{"no-new-privileges:true"}
	conv["tmpfs"] = []string{"/tmp:rw,nosuid,nodev,size=512m,uid=10001,gid=10001,mode=1770"}
	conv["volumes"] = []string{filepath.ToSlash(filepath.Join(s.Root, "secrets/converter.token")) + ":/run/secrets/converter_token:ro"}
	conv["mem_limit"] = "2g"
	conv["cpus"] = 2
	services := object{"postgres": pgService, "redis": rd, "storage": st, "clickhouse": ch, "backend": be, "web": web, "converter": conv}
	gw := service("nginx", "business", "sandbox", "edge")
	ports, aliases := []string{}, []string{}
	seenPorts, seenHosts := map[string]bool{}, map[string]bool{}
	for _, address := range []string{s.PublicURL, s.internalURL()} {
		u, _ := url.Parse(address)
		port := originPort(u)
		if !seenPorts[port] {
			ports = append(ports, port+":"+port)
			seenPorts[port] = true
		}
		if !seenHosts[u.Hostname()] {
			aliases = append(aliases, u.Hostname())
			seenHosts[u.Hostname()] = true
		}
	}
	for _, port := range s.previewPorts() {
		if !seenPorts[port] {
			ports = append(ports, port+":"+port)
			seenPorts[port] = true
		}
	}
	gw["networks"] = object{"business": object{}, "sandbox": object{"aliases": aliases}, "edge": object{}, "preview_ingress": object{"ipv4_address": gatewayIP}}
	gw["ports"] = ports
	gw["volumes"] = []string{filepath.ToSlash(filepath.Join(s.Root, "gateway.conf")) + ":/etc/nginx/conf.d/default.conf:ro", filepath.ToSlash(filepath.Join(s.Root, "tls")) + ":/run/tls:ro"}
	gw["depends_on"] = []string{"backend", "web"}
	services["gateway"] = gw
	if s.Mode == "single" {
		rt := service("daemon", "sandbox")
		rt["environment"] = object{"RUNTIME_DRIVER": "docker", "DATA_ROOT": "/data", "SANDBOX_ROOT": "/data/sandboxes", "DOCKER_HOST_SANDBOX_ROOT": filepath.ToSlash(filepath.Join(s.Data, "runtime/sandboxes")), "HTTP_LISTEN": "0.0.0.0:7410", "AGENT_COMPOSE_RUNTIME_BASE_URL": "http://runtime:7410", "DEFAULT_IMAGE": img("guest"), "DOCKER_DEFAULT_IMAGE": img("guest"), "SSL_CERT_FILE": "/run/tls/ca-bundle.crt"}
		rt["entrypoint"] = []string{"/bin/sh", "-ec"}
		rt["command"] = []string{"export AGENT_COMPOSE_AUTH_TOKEN=\"$(cat /run/secrets/node.token)\"; exec /usr/bin/tini -- /usr/local/bin/agent-compose daemon"}
		rt["volumes"] = append(dataMount("runtime", "/data"), "/var/run/docker.sock:/var/run/docker.sock", filepath.ToSlash(filepath.Join(s.Root, "secrets/node.token"))+":/run/secrets/node.token:ro", filepath.ToSlash(filepath.Join(s.Root, "tls"))+":/run/tls:ro")
		services["runtime"] = rt
		proxy := service("nginx", "sandbox")
		proxy["volumes"] = []string{filepath.ToSlash(filepath.Join(s.Root, "runtime-proxy.conf")) + ":/etc/nginx/conf.d/default.conf:ro", filepath.ToSlash(filepath.Join(s.Root, "tls")) + ":/run/tls:ro"}
		proxy["depends_on"] = []string{"runtime"}
		services["runtime-proxy"] = proxy
	}
	composition := object{"name": s.Project, "services": services, "networks": object{"business": object{"internal": true}, "sandbox": object{"name": s.Project + "_sandbox_direct"}, "conversion": object{"internal": true}, "edge": object{}, "management": object{}, "preview_ingress": object{"internal": true, "ipam": object{"config": []object{{"subnet": s.PreviewSubnet}}}}}}
	if e = writeJSON(filepath.Join(s.Root, "compose.json"), composition); e != nil {
		return e
	}
	if e = privateWrite(filepath.Join(s.Root, "web.conf"), []byte("server { listen 80; root /usr/share/nginx/html; location / { try_files $uri $uri/ /index.html; } }\n")); e != nil {
		return e
	}
	return writeProxy(s)
}
func health(command string) object {
	return object{"test": []string{"CMD-SHELL", command}, "interval": "5s", "timeout": "4s", "retries": 40, "start_period": "10s"}
}

func writeProxy(s *deployment) error {
	text := `map $http_upgrade $connection_upgrade { default upgrade; '' close; }
map $args $signed_object { default 0; ~(^|&)X-Amz-Signature=[0-9a-f]+(&|$) 1; }
map $uri $public_object { default 0; ~^/oss/jingjiaagent/(avatar|spec|repo)/ 1; }
map "$request_method:$public_object:$signed_object" $object_allowed { default 0; ~^(GET|HEAD):1:[01]$ 1; ~^(GET|HEAD|PUT):[01]:1$ 1; }
`
	for _, cidr := range s.TrustedProxyCIDRs {
		text += "set_real_ip_from " + cidr + ";\n"
	}
	if len(s.TrustedProxyCIDRs) > 0 {
		text += "real_ip_header X-Forwarded-For;\nreal_ip_recursive on;\n"
	}
	seen := map[string]bool{}
	for _, address := range []string{s.PublicURL, s.internalURL()} {
		u, _ := url.Parse(address)
		port := originPort(u)
		if seen[port] {
			continue
		}
		seen[port] = true
		server := gatewayServer(port, u.Scheme == "https")
		gateway, _, _ := previewNetworkAddresses(s.PreviewSubnet)
		server = strings.ReplaceAll(server, "proxy_pass http://backend:8888;", "proxy_pass http://backend-preview:8888; proxy_bind "+gateway+"; proxy_set_header X-Forwarded-For $remote_addr; proxy_set_header X-Real-IP $remote_addr; proxy_set_header Forwarded \"\"; proxy_set_header X-JingjiaAgent-Preview-Port \"\";")
		text += server
	}
	text += previewGatewayServer(s)
	if e := privateWrite(filepath.Join(s.Root, "gateway.conf"), []byte(text)); e != nil {
		return e
	}
	return privateWrite(filepath.Join(s.Root, "runtime-proxy.conf"), []byte(`map $http_upgrade $connection_upgrade { default upgrade; '' close; }
server { listen 7411 ssl; ssl_certificate /run/tls/runtime.crt; ssl_certificate_key /run/tls/runtime.key; client_max_body_size 128m; location / { proxy_pass http://runtime:7410; proxy_http_version 1.1; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade; proxy_buffering off; proxy_read_timeout 900s; } }
`))
}

func gatewayServer(port string, encrypted bool) string {
	listen := "listen " + port
	tlsDirectives := ""
	privateRoutes := `
 location /v1/ { return 426; }
 location /mcp { return 426; }
 location = /api/v1/runtime/git-credential { return 426; }
 location ~ ^/api/v1/users/hosts/(install|install-bundle|install-binary|install-status|install-claim|renew-certificate)/?$ { return 426; }
`
	if encrypted {
		listen += " ssl"
		tlsDirectives = "ssl_certificate /run/tls/server.crt; ssl_certificate_key /run/tls/server.key; ssl_protocols TLSv1.2 TLSv1.3;"
		privateRoutes = `
 location /v1/ { proxy_pass http://backend:8888; proxy_http_version 1.1; proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme; proxy_buffering off; proxy_read_timeout 900s; }
 location /mcp { proxy_pass http://backend:8888; proxy_http_version 1.1; proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme; proxy_buffering off; proxy_read_timeout 900s; }
`
	}
	return fmt.Sprintf(`server {
 %s; server_name _;
 %s
 client_max_body_size 128m;
 location /api/ { proxy_pass http://backend:8888; proxy_http_version 1.1; proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto $scheme; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade; proxy_buffering off; proxy_read_timeout 900s; }
 %s
 location /oss/ { if ($object_allowed = 0) { return 403; } proxy_pass http://storage:9000/; proxy_set_header Host $http_host; proxy_set_header Authorization ""; proxy_buffering off; }
 location / { proxy_pass http://web:80; proxy_set_header Host $http_host; }
}
`, listen, tlsDirectives, privateRoutes)
}
