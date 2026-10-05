package proxy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"golang.org/x/sys/unix"
)

var jingjiaAgentNodeVersion = "unbuilt"

type jingjiaAgentNodeSnapshot struct {
	Schema          string  `json:"schema"`
	InstanceID      string  `json:"instance_id"`
	Fingerprint     string  `json:"fingerprint"`
	CapacityID      string  `json:"capacity_id"`
	Hostname        string  `json:"hostname"`
	Arch            string  `json:"arch"`
	OS              string  `json:"os"`
	Version         string  `json:"version"`
	Cores           int32   `json:"cores"`
	Memory          uint64  `json:"memory"`
	SampledAt       int64   `json:"sampled_at"`
	MemoryAvailable *uint64 `json:"memory_available_bytes,omitempty"`
	DiskTotal       *uint64 `json:"storage_total_bytes,omitempty"`
	DiskAvailable   *uint64 `json:"storage_available_bytes,omitempty"`
}

func jingjiaAgentNodeIdentity(root string) (string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(root, "jingjiaagent-node-id")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			return "", createErr
		}
		id := uuid.NewString()
		_, writeErr := file.WriteString(id + "\n")
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return id, nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return "", errors.New("invalid persistent node identity")
	}
	return id, nil
}

func jingjiaAgentMemoryAvailable(total uint64) *uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil
	}
	defer file.Close()
	var physical, available uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			physical = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	// /proc must describe the same Linux Docker host, not a different daemon
	// accessed through DOCKER_HOST. Unknown remains absent.
	if scanner.Err() != nil || physical == 0 || physical > total+total/50 || physical+physical/50 < total || available > physical || available > total {
		return nil
	}
	return &available
}

// Private node metadata. No Docker credentials, container list, paths or
// environment are returned; this endpoint never performs workload mutations.
func RegisterJingjiaAgentNode(app *echo.Echo, root, token string) {
	instance, identityErr := jingjiaAgentNodeIdentity(root)
	app.GET("/internal/jingjiaagent/node", func(c echo.Context) error {
		parts := strings.Fields(c.Request().Header.Get("Authorization"))
		if token == "" || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) != 1 || c.Request().Header.Get("Origin") != "" {
			return c.NoContent(401)
		}
		if identityErr != nil {
			return c.NoContent(503)
		}
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return c.NoContent(503)
		}
		defer docker.Close()
		info, err := docker.Info(ctx)
		if err != nil || info.NCPU <= 0 || info.MemTotal <= 0 || info.ID == "" {
			return c.NoContent(503)
		}
		capacityID := sha256.Sum256([]byte(info.ID))
		fingerprint := sha256.Sum256([]byte(instance + "\x00" + info.ID))
		out := jingjiaAgentNodeSnapshot{Schema: "jingjiaagent.runtime.node.v1", InstanceID: instance, Fingerprint: hex.EncodeToString(fingerprint[:]), CapacityID: hex.EncodeToString(capacityID[:]), Hostname: info.Name, Arch: info.Architecture, OS: info.OSType, Version: jingjiaAgentNodeVersion, Cores: int32(info.NCPU), Memory: uint64(info.MemTotal), SampledAt: time.Now().Unix()}
		// Only the local Unix socket can share /proc with this daemon. Never
		// report this machine's available RAM for a TCP/remote Docker endpoint.
		if strings.HasPrefix(docker.DaemonHost(), "unix://") {
			out.MemoryAvailable = jingjiaAgentMemoryAvailable(out.Memory)
		}
		var disk unix.Statfs_t
		// Mounted runtime-data filesystem capacity; not Docker image-cache space.
		if err := unix.Statfs(root, &disk); err == nil && disk.Bsize > 0 {
			total, available := disk.Blocks*uint64(disk.Bsize), disk.Bavail*uint64(disk.Bsize)
			if total > 0 && available <= total {
				out.DiskTotal = &total
				out.DiskAvailable = &available
			}
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, out)
	})
}
