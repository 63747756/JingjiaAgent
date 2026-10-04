package runtimeadapter

import "github.com/chaitin/MonkeyCode/backend/pkg/taskflow"

// Only the product's built-in authenticated gateway changes origin. User MCP
// endpoints, local commands and legacy environments retain their configuration.
// The resolved address is committed with the encrypted task admission so worker
// retries and subsequent turns do not select a different gateway after reload.
func (c *Client) runtimeMCPConfigs(input []taskflow.McpServerConfig) []taskflow.McpServerConfig {
	if c.agentMCPURL == "" || c.builtinMCPURL == "" {
		return input
	}
	output := append([]taskflow.McpServerConfig(nil), input...)
	for index, item := range input {
		if item.Name != "monkeycode-ai" || item.Type != "http" || item.Url == nil || *item.Url != c.builtinMCPURL ||
			(item.Command != nil && *item.Command != "") {
			continue
		}
		address := c.agentMCPURL
		output[index].Url = &address
	}
	return output
}
