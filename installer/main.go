// JingjiaAgent's release installer deliberately uses only the Go standard library.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var releasePublicKey string // Ed25519, base64; set by the signed release builder.
var installerVersion = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "JingjiaAgent:", err)
		os.Exit(1)
	}
}

type options struct {
	Root, Data, Bundle, ReleaseURL, PublicURL, Mode, Certificate, Key, CA, PreviewURL string
	Offline, VerifyOnly, NonInteractive                                               bool
	Project                                                                           string
}

func run(args []string) error {
	// RHEL-family sudo secure_path commonly omits /usr/local/bin, where the
	// managed Docker CLI is installed. Keep existing command precedence while
	// making that CLI discoverable both during first install and on later runs.
	if runtime.GOOS == "linux" {
		if err := includeCommandDirectory("/usr/local/bin"); err != nil {
			return err
		}
	}
	if len(args) == 0 {
		args = []string{"help"}
	}
	command := args[0]
	if command == "node" {
		return installNode(os.Stdin)
	}
	if command == "version" {
		fmt.Println(installerVersion)
		return nil
	}
	if command == "help" || command == "--help" {
		fmt.Println("JingjiaAgent: install, verify, status, logs, doctor, start, stop, restart, upgrade, backup, restore, tls-renew, version\n安装: jingjiaagent install --bundle DIR --offline --public-url https://HOST\n在线: jingjiaagent install --release-url https://HOST/releases/VERSION/linux-amd64\n已有安装: jingjiaagent COMMAND --root /opt/jingjiaagent\n恢复: jingjiaagent restore --file BACKUP --bundle ORIGINAL_RELEASE --offline（原路径须为空）")
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	o := options{}
	f.StringVar(&o.Root, "root", "/opt/jingjiaagent", "installation directory")
	f.StringVar(&o.Data, "data", "/var/lib/jingjiaagent", "persistent data directory")
	f.StringVar(&o.Bundle, "bundle", "", "release bundle directory")
	f.StringVar(&o.ReleaseURL, "release-url", "", "HTTPS release directory")
	f.StringVar(&o.PublicURL, "public-url", "", "HTTPS console URL")
	f.StringVar(&o.PreviewURL, "preview-url", "", "optional separate wildcard HTTPS preview domain")
	f.StringVar(&o.Mode, "mode", "single", "single or controller")
	f.StringVar(&o.Project, "project", "jingjiaagent", "unique Docker Compose project name")
	f.StringVar(&o.Certificate, "tls-cert", "", "enterprise TLS certificate PEM")
	f.StringVar(&o.Key, "tls-key", "", "enterprise TLS private key PEM")
	f.StringVar(&o.CA, "ca-cert", "", "optional enterprise CA PEM")
	f.BoolVar(&o.Offline, "offline", false, "never download installation materials")
	f.BoolVar(&o.VerifyOnly, "verify-only", false, "verify release without changing Docker or installation state")
	f.BoolVar(&o.NonInteractive, "non-interactive", false, "require all mandatory options")
	backupFile := f.String("file", "", "backup archive path")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if len(f.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "recover" || command == "restore" {
		return recoverDeployment(o, *backupFile)
	}
	if command == "verify" {
		o.VerifyOnly = true
		return install(o)
	}
	if command == "install" || command == "upgrade" {
		if command == "install" && !o.VerifyOnly && o.PublicURL == "" {
			if o.NonInteractive {
				return errors.New("--public-url is required")
			}
			fmt.Print("控制台 HTTPS 地址（例如 https://192.168.1.20）: ")
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil {
				return err
			}
			o.PublicURL = strings.TrimSpace(line)
		}
		if command == "upgrade" {
			return upgrade(o)
		}
		return install(o)
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return err
	}
	s, err := readState(root)
	if err != nil {
		return err
	}
	if command == "start" || command == "stop" || command == "restart" {
		unlock, err := lock(s.Root)
		if err != nil {
			return err
		}
		defer unlock()
	}
	switch command {
	case "tls-renew":
		return renewTLS(s)
	case "status":
		return compose(s, "ps", "--all")
	case "logs":
		return compose(s, "logs", "--tail", "150")
	case "doctor":
		return doctor(s)
	case "start":
		return start(s)
	case "stop":
		return stop(s)
	case "restart":
		if err = stop(s); err != nil {
			return err
		}
		return start(s)
	case "backup":
		return backup(s, *backupFile)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func includeCommandDirectory(directory string) error {
	path := os.Getenv("PATH")
	for _, entry := range filepath.SplitList(path) {
		if filepath.Clean(entry) == filepath.Clean(directory) {
			return nil
		}
	}
	if path != "" {
		path += string(os.PathListSeparator)
	}
	return os.Setenv("PATH", path+directory)
}

func requireLinux() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("首版安装器仅支持 Linux amd64；verify 可在构建机执行")
	}
	if os.Geteuid() != 0 {
		return errors.New("请使用 sudo 运行安装器")
	}
	return nil
}
