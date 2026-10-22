package config

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/63747756/jingjiaagent/backend/consts"
)

// ValidateAgentModel checks the protocol used by the selected native runtime.
func ValidateAgentModel(cli consts.CliName, protocol string) error {
	switch cli {
	case consts.CliNameOpencode:
		if protocol == "" || protocol == string(consts.InterfaceTypeOpenAIChat) || protocol == string(consts.InterfaceTypeOpenAIResponse) || protocol == string(consts.InterfaceTypeAnthropic) {
			return nil
		}
	case consts.CliNameCodex:
		if protocol == string(consts.InterfaceTypeOpenAIResponse) {
			return nil
		}
	case consts.CliNameClaude:
		if protocol == string(consts.InterfaceTypeAnthropic) {
			return nil
		}
	}
	return errors.New("所选模型的接口协议与当前助手执行环境不兼容")
}

// An empty list is deny-all. Resolve every address, including mixed public and
// private DNS answers. The network gateway independently pins the allowed IP.
func (w Work) ValidateModelURL(ctx context.Context, address string) error {
	if !w.Enabled || !w.Offline {
		return nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("离线模型地址无效")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	host := net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	allowed := false
	for _, entry := range w.AllowedModelHosts {
		if strings.EqualFold(entry, host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("模型地址未在内网允许列表中")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(addresses) == 0 {
		return errors.New("内网模型地址解析失败")
	}
	for _, address := range addresses {
		if !address.IP.IsPrivate() && !address.IP.IsLoopback() {
			return errors.New("模型解析到了非内网地址")
		}
	}
	return nil
}
