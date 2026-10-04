package runtimeadapter

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type WorkerService struct {
	client    *Client
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
}

func NewWorkerService(c *Client) *WorkerService {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerService{client: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}
func (s *WorkerService) Name() string { return "Remote runtime worker" }
func (s *WorkerService) Start() error {
	var err error
	s.startOnce.Do(func() {
		defer close(s.done)
		group, ctx := errgroup.WithContext(s.ctx)
		if s.client.registry != nil {
			group.Go(func() error { return s.client.runNodes(ctx) })
		}
		group.Go(func() error {
			tick := time.NewTicker(10 * time.Second)
			defer tick.Stop()
			for {
				if err := s.client.ReconcileCreations(ctx); err != nil && ctx.Err() == nil {
					s.client.logger.WarnContext(ctx, "prepared task reconciliation deferred")
				}
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
				}
			}
		})
		group.Go(func() error { return s.client.RunWorker(ctx) })
		err = group.Wait()
	})
	return err
}
func (s *WorkerService) Stop() error {
	s.cancel()
	select {
	case <-s.done:
		return s.client.Close()
	case <-time.After(10 * time.Second):
		return context.DeadlineExceeded
	}
}
