package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func output(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	b, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("%s 操作失败，请通过 doctor/logs 查看服务状态", name)
	}
	return b, nil
}
func execute(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func composeArgs(s *deployment, args ...string) []string {
	return append([]string{"compose", "--project-directory", s.Root, "-p", s.Project, "-f", filepath.Join(s.Root, "compose.json")}, args...)
}
func compose(s *deployment, args ...string) error {
	return execute("docker", composeArgs(s, args...)...)
}

func validateOptions(o options) error {
	if _, err := allowedHosts(o.AllowedHosts); err != nil {
		return err
	}
	if o.Mode != "single" && o.Mode != "controller" {
		return errors.New("mode must be single or controller")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}$`).MatchString(o.Project) {
		return errors.New("invalid Compose project")
	}
	u, e := httpsURL(o.PublicURL)
	if e != nil {
		return e
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("public URL cannot contain a path")
	}
	if strings.ContainsAny(u.Host, "$;{}\"' \t\r\n") {
		return errors.New("invalid public URL host")
	}
	if o.PreviewURL != "" {
		p, e := httpsURL(o.PreviewURL)
		if e != nil {
			return e
		}
		if p.Path != "" || p.Port() != "" || p.Hostname() == u.Hostname() || strings.HasSuffix(u.Hostname(), "."+p.Hostname()) {
			return errors.New("preview requires a separate wildcard domain")
		}
	}
	for _, p := range []string{o.Root, o.Data} {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" || strings.ContainsAny(p, "\n\r:$") {
			return errors.New("use absolute dedicated installation and data directories")
		}
		if s, e := os.Lstat(p); e == nil && s.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation directories must not be symlinks")
		}
	}
	if o.Root == o.Data || strings.HasPrefix(o.Root, o.Data+string(filepath.Separator)) || strings.HasPrefix(o.Data, o.Root+string(filepath.Separator)) {
		return errors.New("installation and data directories must be separate")
	}
	return nil
}

func preflightDocker() error {
	b, e := output("docker", "info", "--format", "{{json .}}")
	if e != nil {
		return errors.New("Docker daemon 不可用")
	}
	var d struct {
		OSType, Architecture, DockerRootDir, ServerVersion string
		NCPU                                               int
		MemTotal                                           int64
		MemoryLimit, CPUCfsQuota                           bool
	}
	if json.Unmarshal(b, &d) != nil || d.OSType != "linux" || (d.Architecture != "x86_64" && d.Architecture != "amd64") {
		return errors.New("需要 Linux amd64 Docker Engine")
	}
	if !d.MemoryLimit || !d.CPUCfsQuota {
		return errors.New("Docker 缺少内存或 CPU 限制能力，请检查 cgroups")
	}
	major, _ := strconv.Atoi(strings.Split(d.ServerVersion, ".")[0])
	if major < 28 {
		return errors.New("需要 Docker Engine 28 或更新版本")
	}
	if _, e = output("docker", "compose", "version"); e != nil {
		return errors.New("需要 Docker Compose v2 插件")
	}
	return nil
}

func ensureDocker(bundle string, m *manifest) error {
	return ensureDockerAt(bundle, m, managedEnginePaths{
		dir: "/opt/jingjiaagent-engine", cli: "/usr/local/bin/docker",
		compose: "/usr/local/lib/docker/cli-plugins/docker-compose",
		unit:    "/etc/systemd/system/jingjiaagent-docker.service",
	})
}

type managedEnginePaths struct{ dir, cli, compose, unit string }

func ensureDockerAt(bundle string, m *manifest, paths managedEnginePaths) error {
	managed := false
	if cli, e := exec.LookPath("docker"); e == nil {
		target, _ := os.Readlink(cli)
		marker, _ := os.ReadFile(filepath.Join(paths.dir, "release.sha256"))
		managed = cli == paths.cli && target == paths.dir+"/docker/docker" && shaPattern.Match(marker)
		// A healthy engine is reused, including a managed engine from an older
		// release. Only a proven installation-owned partial setup may be resumed.
		if managed && preflightDocker() == nil {
			return nil
		}
		if !managed {
			if _, e = output("docker", "compose", "version"); e != nil {
				if e = installComposeAt(bundle, m, paths.compose); e != nil {
					return e
				}
			}
			return preflightDocker()
		}
	}
	// Never replace an existing engine/containerd belonging to another product.
	for _, name := range []string{"dockerd", "containerd"} {
		if _, e := exec.LookPath(name); e == nil && !managed {
			return errors.New("发现已有容器引擎，请配置现有 Docker；安装器不会覆盖它")
		}
	}
	if _, e := exec.LookPath("systemctl"); e != nil {
		return errors.New("自动安装 Docker 需要 systemd；请先手动准备 Docker")
	}
	if _, e := exec.LookPath("iptables"); e != nil {
		return errors.New("缺少 iptables，请安装本发行版对应的软件包后重试（离线环境使用系统安装介质）")
	}
	a, ok := m.Files["engine/docker.tgz"]
	if !ok {
		return errors.New("发行包未提供 Docker，请准备 Docker Engine 和 Compose 后重试")
	}
	p, e := contained(bundle, "engine/docker.tgz")
	if e != nil {
		return e
	}
	if e = verifyFile(p, a); e != nil {
		return e
	}
	engineDir := paths.dir
	if entries, err := os.ReadDir(engineDir); err == nil && len(entries) > 0 {
		marker, err := os.ReadFile(filepath.Join(engineDir, "release.sha256"))
		if err != nil || string(marker) != a.SHA256 {
			return errors.New("已有引擎目录不属于此安装包，拒绝覆盖")
		}
	} else {
		stage, err := os.MkdirTemp(filepath.Dir(engineDir), ".jingjiaagent-engine-*")
		if err != nil {
			return err
		}
		if e = extractEngine(p, stage); e != nil {
			return e
		}
		if e = privateWrite(filepath.Join(stage, "release.sha256"), []byte(a.SHA256)); e != nil {
			return e
		}
		if _, err = os.Stat(engineDir); err == nil {
			if err = os.Remove(engineDir); err != nil {
				return err
			}
		}
		if e = os.Rename(stage, engineDir); e != nil {
			return e
		}
	}
	if e = installComposeAt(bundle, m, paths.compose); e != nil {
		return e
	}
	if _, e = os.Lstat(paths.cli); !os.IsNotExist(e) && !managed {
		return errors.New("docker executable path already exists")
	}
	if !managed {
		if e = os.Symlink(engineDir+"/docker/docker", paths.cli); e != nil {
			return e
		}
	}
	unit := "[Unit]\nDescription=JingjiaAgent managed Docker Engine\nAfter=network-online.target\nWants=network-online.target\n[Service]\nEnvironment=PATH=" + engineDir + "/docker:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nExecStart=" + engineDir + "/docker/dockerd\nRestart=on-failure\nDelegate=yes\nKillMode=process\nLimitNOFILE=infinity\nTasksMax=infinity\n[Install]\nWantedBy=multi-user.target\n"
	if old, err := os.ReadFile(paths.unit); err == nil && string(old) != unit {
		return errors.New("已有 Docker 服务配置不属于此安装器，拒绝覆盖")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if e = privateWrite(paths.unit, []byte(unit)); e != nil {
		return e
	}
	if e = execute("systemctl", "daemon-reload"); e != nil {
		return e
	}
	if e = execute("systemctl", "enable", "--now", "jingjiaagent-docker.service"); e != nil {
		return e
	}
	for i := 0; i < 20; i++ {
		if e = preflightDocker(); e == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return e
}

func installComposeAt(bundle string, m *manifest, target string) error {
	a, ok := m.Files["engine/docker-compose"]
	if !ok {
		return errors.New("发行包不含 Compose 插件，请先安装 Docker Compose v2")
	}
	path, e := contained(bundle, "engine/docker-compose")
	if e != nil {
		return e
	}
	if e = verifyFile(path, a); e != nil {
		return e
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 && verifyFile(target, a) == nil {
			return nil // Resume after the plugin was copied but the CLI was not.
		}
		return errors.New("已有 Compose 插件不匹配，请检查现有安装；不会覆盖")
	} else if !os.IsNotExist(err) {
		return err
	}
	return copyFile(path, target, 0755)
}

func extractEngine(path, dst string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer gz.Close()
	t := tar.NewReader(gz)
	var total int64
	seen := map[string]bool{}
	for {
		h, e := t.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		name := strings.TrimSuffix(h.Name, "/")
		p, e := contained(dst, name)
		if e != nil {
			return e
		}
		if seen[name] {
			return errors.New("duplicate engine archive entry")
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			if e = os.MkdirAll(p, 0755); e != nil {
				return e
			}
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 512<<20 {
			return errors.New("invalid engine archive entry")
		}
		total += h.Size
		if total > 1<<30 {
			return errors.New("engine archive too large")
		}
		if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			return e
		}
		out, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if e != nil {
			return e
		}
		_, e = io.CopyN(out, t, h.Size)
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
		return e
	}
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if e != nil {
		return e
	}
	name := out.Name()
	defer os.Remove(name)
	_, e = io.Copy(out, in)
	if e == nil {
		e = out.Chmod(mode)
	}
	ce := out.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, dst)
}

func install(o options) error {
	if !o.VerifyOnly {
		if e := requireLinux(); e != nil {
			return e
		}
		if e := validateOptions(o); e != nil {
			return e
		}
	}
	bundle, m, e := acquire(o)
	if e != nil {
		return e
	}
	if o.VerifyOnly {
		fmt.Printf("发行包 %s linux/%s 签名及材料校验通过；未修改 Docker。\n", m.Version, m.Architecture)
		return nil
	}
	unlock, e := lock(o.Root)
	if e != nil {
		return e
	}
	defer unlock()
	if e = ensureDocker(bundle, m); e != nil {
		return e
	}
	if old, e := readState(o.Root); e == nil {
		if old.Version != m.Version {
			return errors.New("已有其他版本，请使用 upgrade")
		}
		if e = validateRetryOptions(old, o); e != nil {
			return e
		}
		if old.Phase == "ready" {
			return doctor(old)
		}
		if old.Phase != "preparing" && old.Phase != "starting" {
			return errors.New("已有恢复或升级操作尚未完成，请使用 start 继续，不能通过 install 覆盖维护状态")
		}
	} else {
		for _, path := range []string{o.Root, o.Data} {
			entries, err := os.ReadDir(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			for _, entry := range entries {
				if path == o.Root && (entry.Name() == "downloads" || entry.Name() == ".operation.lock") {
					continue
				}
				return errors.New("首次安装要求独立空目录，发现已有数据: " + path)
			}
		}
		b, err := output("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+o.Project)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != "" {
			return errors.New("同名 Compose 项目已存在")
		}
	}
	s := &deployment{Schema: 1, Root: o.Root, Data: o.Data, PublicURL: strings.TrimRight(o.PublicURL, "/"), PreviewURL: o.PreviewURL, Mode: o.Mode, Project: o.Project, Version: m.Version, DatabaseGeneration: m.DatabaseGeneration, Images: m.Images, Phase: "preparing"}
	s.AllowedHosts, _ = allowedHosts(o.AllowedHosts)
	s.Sequence = m.Sequence
	if old, err := readState(o.Root); err == nil {
		s.AllowedHosts = old.AllowedHosts
		if old.Phase != "preparing" {
			if err = checkSecrets(old); err != nil {
				return err
			}
		}
	}
	if e = saveState(s); e != nil {
		return e
	}
	fmt.Println("校验并准备固定版本镜像…")
	if e = loadImages(bundle, m, o.Offline); e != nil {
		return e
	}
	s.Images = m.Images
	if e = copyRelease(bundle, s, m); e != nil {
		return e
	}
	if e = prepareTLS(s, o); e != nil {
		return e
	}
	if e = render(s); e != nil {
		return e
	}
	for _, name := range []string{"postgres", "redis", "storage", "clickhouse", "backend", "runtime"} {
		if e = os.MkdirAll(filepath.Join(s.Data, name), 0750); e != nil {
			return e
		}
	}
	s.Phase = "starting"
	if e = saveState(s); e != nil {
		return e
	}
	if e = startRendered(s); e != nil {
		return e
	}
	s.Phase = "ready"
	if e = saveState(s); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e == nil {
		e = copyFile(exe, filepath.Join(s.Root, "jingjiaagent"), 0755)
	}
	if e != nil {
		return e
	}
	fmt.Printf("安装完成：%s/setup\n初始化凭证保存在 %s（仅管理员可读）。\n内部 CA：%s；请先在客户端信任，或使用企业证书。\n", s.PublicURL, filepath.Join(s.Root, "secrets/setup.token"), filepath.Join(s.Root, "tls/ca.crt"))
	return nil
}

func validateRetryOptions(old *deployment, o options) error {
	if old.PublicURL != strings.TrimRight(o.PublicURL, "/") || old.Mode != o.Mode || old.Data != o.Data || old.Project != o.Project || old.PreviewURL != o.PreviewURL {
		return errors.New("安装参数与现有部署不一致，请保留原地址、模式、数据目录、--project 和 --preview-url")
	}
	return nil
}

func loadImages(bundle string, m *manifest, offline bool) error {
	loaded := map[string]bool{}
	for name, im := range m.Images {
		if offline || im.Ref == "" {
			if !loaded[im.Archive] {
				p, e := contained(bundle, im.Archive)
				if e != nil {
					return e
				}
				if e = verifyFile(p, m.Files[im.Archive]); e != nil {
					return e
				}
				if e = execute("docker", "load", "-i", p); e != nil {
					return e
				}
				loaded[im.Archive] = true
			}
		} else {
			if e := execute("docker", "pull", im.Ref); e != nil {
				return e
			}
		}
		ref := im.Ref
		if offline || ref == "" {
			ref = im.LocalTag
		}
		if ref == "" {
			return errors.New("missing local image tag")
		}
		b, e := output("docker", "image", "inspect", ref)
		if e != nil {
			return e
		}
		var list []struct {
			ID               string `json:"Id"`
			Os, Architecture string
		}
		if json.Unmarshal(b, &list) != nil || len(list) != 1 {
			return errors.New("invalid image inspection")
		}
		imID := list[0].ID
		if list[0].Os != "linux" || list[0].Architecture != m.Architecture {
			return fmt.Errorf("镜像身份或架构不一致: %s", name)
		}
		if imID != im.ID && imID != im.ConfigID {
			if _, e = verifyPortableImage(ref, im.ConfigID); e != nil {
				return fmt.Errorf("镜像身份不一致: %s: %w", name, e)
			}
		}
		im.ID = imID
		m.Images[name] = im
	}
	return nil
}
func copyRelease(bundle string, s *deployment, m *manifest) error {
	for name, a := range m.Files {
		if !a.Online || strings.HasPrefix(name, "images/") {
			continue
		}
		src, e := contained(bundle, name)
		if e != nil {
			return e
		}
		if e = verifyFile(src, a); e != nil {
			return e
		}
		dst, e := contained(filepath.Join(s.Root, "release"), name)
		if e != nil {
			return e
		}
		if verifyFile(dst, a) == nil {
			continue
		}
		mode := os.FileMode(0600)
		if name == "jingjiaagent" {
			mode = 0755
		}
		if e = copyFile(src, dst, mode); e != nil {
			return e
		}
	}
	for _, name := range []string{"manifest.json", "manifest.sig"} {
		if e := copyFile(filepath.Join(bundle, name), filepath.Join(s.Root, "release", name), 0600); e != nil {
			return e
		}
	}
	return nil
}
