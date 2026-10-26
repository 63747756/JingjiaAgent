//go:build linux

package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveredPreviewNetworkUsesDestinationInventory(t *testing.T) {
	const savedSubnet = "172.30.255.0/29"
	const networkID = "0123456789abcdef"
	for _, tc := range []struct {
		name         string
		own          bool
		foreign      bool
		unowned      bool
		connected    bool
		hostConflict bool
		wantError    bool
		wantReuse    bool
		wantDrop     bool
	}{
		{name: "foreign-network-occupies-backup-subnet", foreign: true},
		{name: "existing-managed-network-is-reusable", own: true, wantReuse: true},
		{name: "managed-network-conflicts-with-destination", own: true, foreign: true, wantDrop: true},
		{name: "managed-network-conflicts-with-host-interface", own: true, hostConflict: true, wantDrop: true},
		{name: "same-name-unmanaged-network-is-not-removed", own: true, unowned: true, wantError: true},
		{name: "attached-network-is-not-removed", own: true, connected: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			subnet := savedSubnet
			occupied := netip.MustParsePrefix("172.30.255.0/24")
			if tc.hostConflict {
				addresses, err := net.InterfaceAddrs()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, address := range addresses {
					prefix, err := netip.ParsePrefix(address.String())
					if err == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() {
						subnet = netip.PrefixFrom(prefix.Addr(), 29).Masked().String()
						occupied, found = prefix.Masked(), true
						break
					}
				}
				if !found {
					t.Skip("requires a private IPv4 host interface")
				}
			}
			networks := []object{}
			if tc.own {
				labels := object{"com.docker.compose.project": "recovery", "com.docker.compose.network": "preview_ingress"}
				if tc.unowned {
					labels = object{}
				}
				containers := object{}
				if tc.connected {
					containers["stopped-container"] = object{"Name": "retained"}
				}
				networks = append(networks, object{"Id": networkID, "Name": "recovery_preview_ingress", "Driver": "bridge", "Internal": true, "Labels": labels, "Containers": containers, "IPAM": object{"Config": []object{{"Subnet": subnet}}}})
			}
			if tc.foreign {
				networks = append(networks, object{"Id": "foreign-network", "Name": "other-platform", "IPAM": object{"Config": []object{{"Subnet": "172.30.255.0/24"}}}})
			}
			inventory, err := json.Marshal(networks)
			if err != nil {
				t.Fatal(err)
			}
			inventoryPath := filepath.Join(root, "networks.json")
			if err := os.WriteFile(inventoryPath, inventory, 0600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "docker.log")
			if err := os.WriteFile(filepath.Join(root, "docker"), []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$RECOVERY_NETWORK_LOG"
case "$*" in
  "network ls -q") echo inventory; exit 0;;
  "network inspect inventory") cat "$RECOVERY_NETWORK_INVENTORY"; exit 0;;
  "network rm "*) exit 0;;
esac
exit 1
`), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+":"+os.Getenv("PATH"))
			t.Setenv("RECOVERY_NETWORK_LOG", logPath)
			t.Setenv("RECOVERY_NETWORK_INVENTORY", inventoryPath)
			s := &deployment{Project: "recovery", PreviewSubnet: subnet}
			err = prepareRecoveredPreviewNetwork(s)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected recovery network result: %v", err)
			}
			commands := string(mustReadTest(t, logPath))
			if strings.Contains(commands, "network rm "+networkID) != tc.wantDrop {
				t.Fatalf("unexpected network deletion: %s", commands)
			}
			if tc.wantError || tc.wantReuse {
				if s.PreviewSubnet != subnet {
					t.Fatal("existing network was unexpectedly replaced")
				}
				return
			}
			candidate, err := netip.ParsePrefix(s.PreviewSubnet)
			if err != nil || candidate.Overlaps(occupied) {
				t.Fatalf("restored network still conflicts with destination: %s", s.PreviewSubnet)
			}
		})
	}
}
