package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Full-host recovery can move an installation onto a different network. A
// subnet saved on the source host is not a reservation on the destination.
func prepareRecoveredPreviewNetwork(s *deployment) error {
	raw, err := output("docker", "network", "ls", "-q")
	if err != nil {
		return err
	}
	type network struct {
		ID, Name, Driver string
		Internal         bool
		Labels, Options  map[string]string
		Containers       map[string]json.RawMessage
		IPAM             struct{ Config []struct{ Subnet string } }
	}
	var networks []network
	if ids := strings.Fields(string(raw)); len(ids) > 0 {
		raw, err = output("docker", append([]string{"network", "inspect"}, ids...)...)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &networks); err != nil {
			return err
		}
	}
	var owned *network
	for i := range networks {
		if networks[i].Name != s.Project+"_preview_ingress" {
			continue
		}
		owned = &networks[i]
		if owned.Labels["com.docker.compose.project"] != s.Project || owned.Labels["com.docker.compose.network"] != "preview_ingress" {
			return errors.New("恢复目标存在同名非本安装管理的预览网络，请先调整网络名称")
		}
		if len(owned.Containers) != 0 {
			return errors.New("恢复目标预览网络仍连接容器，不能重新配置")
		}
	}
	if owned != nil && owned.Driver == "bridge" && owned.Internal && len(owned.IPAM.Config) == 1 {
		subnet := owned.IPAM.Config[0].Subnet
		if _, _, err := previewNetworkAddresses(subnet); err == nil {
			candidate := netip.MustParsePrefix(subnet)
			conflict := false
			for _, n := range networks {
				if n.Name == owned.Name {
					continue
				}
				for _, cfg := range n.IPAM.Config {
					if prefix, err := netip.ParsePrefix(cfg.Subnet); err == nil && candidate.Overlaps(prefix) {
						conflict = true
					}
				}
			}
			bridge := owned.Options["com.docker.network.bridge.name"]
			if bridge == "" && len(owned.ID) >= 12 {
				bridge = "br-" + owned.ID[:12]
			}
			interfaces, err := net.Interfaces()
			if err != nil {
				return err
			}
			for _, iface := range interfaces {
				if iface.Name == bridge {
					continue // The existing Docker bridge is the network itself.
				}
				addresses, err := iface.Addrs()
				if err != nil {
					return err
				}
				for _, address := range addresses {
					if prefix, err := netip.ParsePrefix(address.String()); err == nil && candidate.Overlaps(prefix) {
						conflict = true
					}
				}
			}
			if !conflict {
				s.PreviewSubnet = subnet
				return nil
			}
		}
	}
	// Select before removing an unusable empty network so allocation failure
	// leaves it untouched. The inventory includes all networks, not just those
	// with running containers, as well as the destination host's interfaces.
	next := *s
	next.PreviewSubnet = ""
	if err = ensurePreviewNetwork(&next); err != nil {
		return err
	}
	if owned != nil {
		if owned.ID == "" {
			return errors.New("恢复目标预览网络缺少 Docker 标识")
		}
		if err = execute("docker", "network", "rm", owned.ID); err != nil {
			return fmt.Errorf("无法重新配置恢复目标的空预览网络: %w", err)
		}
	}
	s.PreviewSubnet = next.PreviewSubnet
	return nil
}
