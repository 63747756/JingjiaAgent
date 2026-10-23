package runtimeadapter

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"time"

	"github.com/63747756/jingjiaagent/backend/config"
	"github.com/63747756/jingjiaagent/backend/pkg/runtimenode"
	"github.com/lib/pq"
)

func (c *Client) NodeStore() *runtimenode.Store { return c.nodeStore }
func (c *Client) engine(id string) *Engine {
	c.engineMu.RLock()
	defer c.engineMu.RUnlock()
	return c.engines[id]
}
func (c *Client) node(id string) config.RuntimeNode {
	c.engineMu.RLock()
	defer c.engineMu.RUnlock()
	return c.nodes[id]
}
func (c *Client) hasNode(id string) bool {
	c.engineMu.RLock()
	defer c.engineMu.RUnlock()
	_, ok := c.nodes[id]
	return ok
}
func (c *Client) nodesSnapshot() map[string]config.RuntimeNode {
	c.engineMu.RLock()
	defer c.engineMu.RUnlock()
	out := make(map[string]config.RuntimeNode, len(c.nodes))
	for id, n := range c.nodes {
		out[id] = n
	}
	return out
}

// Refresh updates immutable connection objects, never a transport used by an
// in-flight stream. A process verifies a replacement before making it available.
func (c *Client) RefreshNodes(ctx context.Context) error {
	if c.nodeStore == nil {
		return nil
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	records, e := c.nodeStore.List(ctx)
	if e != nil {
		return e
	}
	c.engineMu.Lock()
	if c.nodeRevisions == nil {
		c.nodeRevisions = map[string]int64{}
	}
	if c.nodeMaterials == nil {
		c.nodeMaterials = map[string]runtimenode.Material{}
	}
	c.engineMu.Unlock()
	for _, r := range records {
		id := r.Config.ID
		if r.Revoked {
			c.verifyNode(id, false)
			c.engineMu.Lock()
			delete(c.nodes, id)
			delete(c.engines, id)
			delete(c.nodeMaterials, id)
			delete(c.nodeRevisions, id)
			c.engineMu.Unlock()
			continue
		}
		c.engineMu.RLock()
		revision := c.nodeRevisions[id]
		prior, exists := c.nodes[id]
		material := c.nodeMaterials[id]
		c.engineMu.RUnlock()
		if revision == r.Revision {
			continue
		}
		// Draining and readiness updates do not change a connection or interrupt
		// pinned tasks. Admission reads accepting directly from the database.
		if exists && reflect.DeepEqual(prior, r.Config) && material == r.Material {
			c.engineMu.Lock()
			c.nodeRevisions[id] = r.Revision
			c.engineMu.Unlock()
			continue
		}
		if r.Material.CA == "" && !r.Imported {
			continue
		}
		n, err := NewEngineMaterial(r.Config, r.Material, !r.Imported)
		if err != nil {
			c.verifyNode(id, false)
			continue
		}
		// Startup also needs offline nodes in the registry for pinned tasks. They
		// remain unverified until syncNode succeeds, so no work can be sent to them.
		if c.hasNode(id) && c.registry != nil {
			snap, err := n.nodeSnapshot(ctx)
			if err != nil {
				c.verifyNode(id, false)
				continue
			}
			if err = c.ledger.bindNode(ctx, id, snap); err != nil {
				c.verifyNode(id, false)
				continue
			}
		}
		c.verifyNode(id, false)
		c.engineMu.Lock()
		c.nodes[id] = r.Config
		c.engines[id] = n
		c.nodeRevisions[id] = r.Revision
		c.nodeMaterials[id] = r.Material
		c.engineMu.Unlock()
	}
	return nil
}

// SQL is the authority. LISTEN is an accelerator; polling recovers missed
// notifications, disconnected listeners, and a newly started backend process.
func (c *Client) watchNodes(ctx context.Context) error {
	if c.nodeStore == nil {
		<-ctx.Done()
		return nil
	}
	listener := pq.NewListener(c.nodeDSN, time.Second, 30*time.Second, nil)
	defer listener.Close()
	_ = listener.Listen("jingjiaagent_runtime_nodes")
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-listener.Notify:
		case <-tick.C:
		}
		if e := c.RefreshNodes(ctx); e == nil {
			_ = c.SyncNodes(ctx)
		} else if c.logger != nil {
			c.logger.WarnContext(ctx, "runtime node refresh deferred")
		}
	}
}
func (c *Client) RuntimeNodeIDs(ctx context.Context) ([]string, error) {
	if e := c.RefreshNodes(ctx); e != nil {
		return nil, e
	}
	ids := []string{}
	for id := range c.nodesSnapshot() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
func (c *Client) acceptingNode(ctx context.Context, id string) (bool, error) {
	if c.nodeStore == nil {
		return true, nil
	}
	r, e := c.nodeStore.Get(ctx, id)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return !r.Revoked && r.Accepting && r.Phase == "ready", nil
}
