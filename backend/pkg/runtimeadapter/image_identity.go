package runtimeadapter

import (
	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
	v2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// Only translate a digest proven to identify the same bundled image. Arbitrary
// custom images are never substituted. Persisted requests retain their identity.
func runtimeImage(node config.RuntimeNode, requested string) string {
	if requested == "" {
		requested = node.GuestImage
	}
	if requested == node.GuestImage && node.InstalledGuestImage != "" {
		return node.InstalledGuestImage
	}
	return requested
}
func (c *Client) projectSpec(e Environment, t taskflow.CreateTaskReq) (*v2.ProjectSpec, error) {
	node := c.node(e.NodeID)
	e.Request.ImageURL = runtimeImage(node, e.Request.ImageURL)
	return projectSpec(e, t, node.GuestImage)
}
