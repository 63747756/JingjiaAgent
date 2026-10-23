package config

import (
	"errors"
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

// ValidateModelURL accepts configured HTTP(S) providers on either network.
// Network reachability belongs to the deployment, not the work product.
func ValidateModelURL(address string) error {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("模型地址无效，请填写 HTTP(S) 接口地址")
	}
	return nil
}
