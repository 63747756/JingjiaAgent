package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type nodePayload struct {
	AllowedHosts []string `json:"allowed_hosts"`
	NodeID       string   `json:"node_id"`
	Endpoint     string   `json:"endpoint"`
	Listen       string   `json:"listen"`
	PlatformURL  string   `json:"platform_url"`
	PlatformCA   string   `json:"platform_ca"`
	Ticket       string   `json:"ticket"`
	Renewal      string   `json:"renewal"`
	Manifest     struct {
		Architecture string            `json:"architecture"`
		SHA256       string            `json:"sha256"`
		Daemon       string            `json:"daemon_image"`
		Guest        string            `json:"guest_image"`
		Proxy        string            `json:"proxy_image"`
		DockerImages map[string]string `json:"docker_images"`
	} `json:"manifest"`
}

func (p nodePayload) request(path string, value any) (*http.Response, error) {
	var body io.Reader
	if value != nil {
		b, e := json.Marshal(value)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	method := "GET"
	if value != nil {
		method = "POST"
	}
	req, e := http.NewRequest(method, strings.TrimRight(p.PlatformURL, "/")+"/api/v1/users/hosts/"+path, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return nil, errors.New("cannot reach platform using verified HTTPS")
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, fmt.Errorf("platform returned %d; refresh binding if expired", response.StatusCode)
	}
	return response, nil
}
func installNode(input io.Reader) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if e := preflightDocker(); e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(input, 512<<10))
	if e != nil {
		return e
	}
	decoded, e := base64.StdEncoding.DecodeString(string(b))
	if e != nil {
		return e
	}
	var p nodePayload
	if e = json.Unmarshal(decoded, &p); e != nil {
		return e
	}
	if !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(p.NodeID) || p.Manifest.Architecture != "amd64" || !shaPattern.MatchString(p.Manifest.SHA256) {
		return errors.New("invalid node enrollment")
	}
	if _, e = httpsURL(p.PlatformURL); e != nil {
		return e
	}
	endpoint, e := httpsURL(p.Endpoint)
	if e != nil {
		return e
	}
	if _, _, e = net.SplitHostPort(p.Listen); e != nil {
		return e
	}
	root := "/opt/jingjiaagent-nodes/" + p.NodeID
	project := "jingjiaagent-node-" + strings.ReplaceAll(p.NodeID, "-", "")
	unlock, e := lock(root)
	if e != nil {
		return e
	}
	defer unlock()
	archive := filepath.Join(root, "images.tar")
	response, e := p.request("install-bundle?token="+url.QueryEscape(p.Ticket), nil)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(root, ".images-*")
	if e != nil {
		response.Body.Close()
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	hash := sha256.New()
	_, e = io.Copy(io.MultiWriter(f, hash), io.LimitReader(response.Body, 32<<30))
	response.Body.Close()
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != p.Manifest.SHA256 {
		return errors.New("node image archive checksum mismatch")
	}
	if e = os.Rename(tmp, archive); e != nil {
		return e
	}
	if e = execute("docker", "load", "-i", archive); e != nil {
		return e
	}
	for _, ref := range []*string{&p.Manifest.Daemon, &p.Manifest.Guest, &p.Manifest.Proxy} {
		if !strings.HasPrefix(*ref, "sha256:") || !shaPattern.MatchString(strings.TrimPrefix(*ref, "sha256:")) {
			return errors.New("invalid node image identity")
		}
		raw, e := output("docker", "image", "inspect", *ref)
		if e != nil {
			portable := p.Manifest.DockerImages[*ref]
			if portable == "" {
				return e
			}
			*ref = portable
			raw, e = output("docker", "image", "inspect", *ref)
		}
		if e != nil {
			return e
		}
		var image []struct {
			ID               string `json:"Id"`
			Os, Architecture string
		}
		if json.Unmarshal(raw, &image) != nil || len(image) != 1 || image[0].ID != *ref || image[0].Os != "linux" || image[0].Architecture != "amd64" {
			return errors.New("node image verification failed")
		}
	}
	keyPath := filepath.Join(root, "node.key")
	certPath := filepath.Join(root, "node.crt")
	keyData, e := os.ReadFile(keyPath)
	var key *rsa.PrivateKey
	if os.IsNotExist(e) {
		key, e = rsa.GenerateKey(rand.Reader, 3072)
		if e != nil {
			return e
		}
		keyData = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		if e = privateWrite(keyPath, keyData); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else {
		block, _ := pem.Decode(keyData)
		if block == nil {
			return errors.New("invalid existing node key")
		}
		key, e = x509.ParsePKCS1PrivateKey(block.Bytes)
		if e != nil {
			return e
		}
	}
	cert, e := os.ReadFile(certPath)
	if os.IsNotExist(e) {
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return e
		}
		leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: p.NodeID}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if ip := net.ParseIP(endpoint.Hostname()); ip != nil {
			leaf.IPAddresses = []net.IP{ip}
		} else {
			leaf.DNSNames = []string{endpoint.Hostname()}
		}
		der, e := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
		if e != nil {
			return e
		}
		cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if e = privateWrite(certPath, cert); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	if e = validateTLS(p.Endpoint, certPath, keyPath); e != nil {
		return e
	}
	proof := sha256.Sum256([]byte("jingjiaagent-enroll:" + p.Ticket + ":" + p.NodeID))
	signature, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, proof[:])
	if e != nil {
		return e
	}
	response, e = p.request("install-claim", object{"token": p.Ticket, "certificate": string(cert), "signature": base64.StdEncoding.EncodeToString(signature), "guest_image": p.Manifest.Guest})
	if e != nil {
		return e
	}
	var claim struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"node_token"`
		} `json:"data"`
	}
	e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&claim)
	response.Body.Close()
	if e != nil || claim.Code != 0 || claim.Data.Token == "" {
		return errors.New("node claim failed")
	}
	for name, value := range map[string]string{"node.token": claim.Data.Token, "node.endpoint": p.Endpoint, "node.platform": p.PlatformURL, "node.id": p.NodeID, "renew.py": p.Renewal, "platform.ca": p.PlatformCA} {
		if name == "node.token" {
			if old, err := os.ReadFile(filepath.Join(root, name)); err == nil && string(old) != value {
				return errors.New("existing node token differs; identity will not be overwritten")
			}
		}
		if e = privateWrite(filepath.Join(root, name), []byte(value)); e != nil {
			return e
		}
	}
	// The same configuration paths are retained for restarts and certificate renewal.
	nginx := `events {} http { map $http_upgrade $connection_upgrade { default upgrade; '' close; } server { listen 7443 ssl; ssl_certificate /run/jingjiaagent/node.crt; ssl_certificate_key /run/jingjiaagent/node.key; client_max_body_size 128m; location / { proxy_pass http://daemon:7410; proxy_http_version 1.1; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade; proxy_buffering off; proxy_read_timeout 900s; } } }`
	if e = privateWrite(filepath.Join(root, "nginx.conf"), []byte(nginx)); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(root, "data"), 0700); e != nil {
		return e
	}
	services := object{
		"daemon": object{"image": p.Manifest.Daemon, "pull_policy": "never", "restart": "unless-stopped", "container_name": project + "-daemon", "entrypoint": []string{"/bin/sh", "-ec"}, "command": []string{"cat /etc/ssl/certs/ca-certificates.crt /run/jingjiaagent/platform.ca > /tmp/platform-ca.pem; python3 /run/jingjiaagent/renew.py & export AGENT_COMPOSE_AUTH_TOKEN=\"$$(cat /run/jingjiaagent/node.token)\"; exec /usr/bin/tini -- /usr/local/bin/agent-compose daemon"}, "environment": object{"RUNTIME_DRIVER": "docker", "DATA_ROOT": "/data", "SANDBOX_ROOT": "/data/sandboxes", "DOCKER_HOST_SANDBOX_ROOT": root + "/data/sandboxes", "HTTP_LISTEN": "0.0.0.0:7410", "AGENT_COMPOSE_RUNTIME_BASE_URL": "http://daemon:7410", "DEFAULT_IMAGE": p.Manifest.Guest, "DOCKER_DEFAULT_IMAGE": p.Manifest.Guest, "SSL_CERT_FILE": "/tmp/platform-ca.pem"}, "volumes": []string{"/var/run/docker.sock:/var/run/docker.sock", root + "/data:/data", root + ":/run/jingjiaagent"}},
		"proxy":  object{"image": p.Manifest.Proxy, "pull_policy": "never", "restart": "unless-stopped", "container_name": project + "-proxy", "entrypoint": []string{"nginx", "-c", "/run/jingjiaagent/nginx.conf", "-g", "daemon off;"}, "ports": []string{p.Listen + ":7443"}, "volumes": []string{root + ":/run/jingjiaagent:ro"}, "depends_on": []string{"daemon"}},
	}
	platform, _ := url.Parse(p.PlatformURL)
	port := platform.Port()
	if port == "" {
		port = "443"
	}
	allowed := append(append([]string{}, p.AllowedHosts...), net.JoinHostPort(platform.Hostname(), port))
	if _, e = allowedHosts(strings.Join(allowed, ",")); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(root, "egress.json"), object{"allowed_hosts": allowed}); e != nil {
		return e
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	if e = copyFile(executable, filepath.Join(root, "jingjiaagent"), 0755); e != nil {
		return e
	}
	services["egress"] = object{"image": p.Manifest.Daemon, "pull_policy": "never", "restart": "unless-stopped", "entrypoint": []string{"/run/jingjiaagent/jingjiaagent", "egress", "/run/jingjiaagent/egress.json"}, "networks": []string{"runtime", "outbound"}, "volumes": []string{root + ":/run/jingjiaagent:ro"}, "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "mem_limit": "256m", "cpus": 1}
	// Guest NO_PROXY uses the common runtime service name for native CLI access.
	// Give a remote daemon the same address as a single-host installation.
	services["daemon"].(object)["networks"] = object{"runtime": object{"aliases": []string{"runtime"}}}
	services["proxy"].(object)["networks"] = []string{"runtime", "outbound"}
	env := services["daemon"].(object)["environment"].(object)
	env["AGENT_COMPOSE_RUNTIME_BASE_URL"] = "http://runtime:7410"
	env["JINGJIAAGENT_OFFLINE"] = "1"
	env["JINGJIAAGENT_OFFLINE_PROXY"] = "http://egress:3128"
	env["HTTP_PROXY"] = "http://egress:3128"
	env["HTTPS_PROXY"] = "http://egress:3128"
	env["NO_PROXY"] = "localhost,127.0.0.1,::1,daemon,proxy"
	for _, service := range services {
		service.(object)["labels"] = object{"jingjiaagent.runtime.node": p.NodeID}
	}
	for _, name := range []string{project + "-daemon", project + "-proxy"} {
		if raw, err := output("docker", "inspect", name); err == nil {
			var rows []struct {
				Config struct{ Labels map[string]string }
			}
			if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].Config.Labels["jingjiaagent.runtime.node"] != p.NodeID {
				return errors.New("existing container belongs to a different installation")
			}
		}
	}
	if e = writeJSON(filepath.Join(root, "compose.json"), object{"name": project, "services": services, "networks": object{"runtime": object{"internal": true}, "outbound": object{}}}); e != nil {
		return e
	}
	state := &deployment{Root: root, Project: project}
	if e = compose(state, "up", "-d"); e != nil {
		return e
	}
	snapshotCode := `import json,pathlib,urllib.request; r=urllib.request.Request('http://127.0.0.1:7410/internal/jingjiaagent/node',headers={'Authorization':'Bearer '+pathlib.Path('/run/jingjiaagent/node.token').read_text().strip()});s=json.load(urllib.request.urlopen(r,timeout=3));print(json.dumps({k:s[k] for k in ['instance_id','fingerprint']}))`
	for attempt := 0; attempt < 30; attempt++ {
		raw, e := output("docker", "exec", project+"-daemon", "python3", "-c", snapshotCode)
		if e == nil {
			var snap object
			if json.Unmarshal(raw, &snap) == nil {
				snap["token"] = p.Ticket
				response, e := p.request("install-status", snap)
				if e == nil {
					var result struct {
						Data struct {
							Ready bool `json:"ready"`
						} `json:"data"`
					}
					err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result)
					response.Body.Close()
					if err == nil && result.Data.Ready {
						fmt.Println("宿主机已绑定；HTTPS 身份和后端心跳确认通过。")
						return nil
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("服务已保留，后端未确认节点；检查宿主机地址和端口后在本机重试")
}
