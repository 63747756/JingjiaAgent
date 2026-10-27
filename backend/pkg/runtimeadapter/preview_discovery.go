package runtimeadapter

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

type discoveredService struct {
	detectedPort
	State string
}
type serviceDiscovery struct {
	Services       []discoveredService
	Uncertain      bool
	UncertainPorts []detectedPort
}
type discoveryEntry struct {
	result    serviceDiscovery
	err       error
	completed time.Time
	done      chan struct{}
}
type previewDiscoveryCache struct {
	mu      sync.Mutex
	entries map[string]*discoveryEntry
}

// Cache only observations, never routing/access decisions. Work is coalesced
// per sandbox and bounded independently of any one viewer disconnecting.
func (c *Client) discoverHTTPServices(ctx context.Context, env Environment, engine *Engine) (serviceDiscovery, error) {
	key := env.ID + ":" + env.SandboxID
	cache := &c.previewDiscovery
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[string]*discoveryEntry{}
	}
	entry := cache.entries[key]
	if entry != nil && !entry.completed.IsZero() && time.Since(entry.completed) < 5*time.Second {
		cache.mu.Unlock()
		return entry.result, entry.err
	}
	if entry == nil || !entry.completed.IsZero() {
		prior := serviceDiscovery{}
		if entry != nil {
			prior = entry.result
		}
		for k, old := range cache.entries {
			if !old.completed.IsZero() && time.Since(old.completed) > 5*time.Minute {
				delete(cache.entries, k)
			}
		}
		// Bound retained snapshots without introducing an all-workspace timer.
		if len(cache.entries) >= 256 {
			for k, old := range cache.entries {
				if !old.completed.IsZero() {
					delete(cache.entries, k)
					break
				}
			}
		}
		entry = &discoveryEntry{done: make(chan struct{})}
		cache.entries[key] = entry
		go func(current *discoveryEntry) {
			probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			result, err := c.scanHTTPServices(probeCtx, env, engine, prior)
			cache.mu.Lock()
			if err != nil {
				result = prior
			}
			current.result, current.err, current.completed = result, err, time.Now()
			close(current.done)
			cache.mu.Unlock()
		}(entry)
	}
	cache.mu.Unlock()
	select {
	case <-ctx.Done():
		return serviceDiscovery{}, ctx.Err()
	case <-entry.done:
		return entry.result, entry.err
	}
}

func (c *Client) scanHTTPServices(ctx context.Context, env Environment, engine *Engine, prior serviceDiscovery) (serviceDiscovery, error) {
	detected, err := c.discoverPreviewPorts(ctx, env, engine)
	if err != nil {
		return serviceDiscovery{}, err
	}
	known := map[int32]bool{}
	for _, service := range prior.Services {
		known[service.Port] = true
	}
	base, _ := url.Parse(c.preview.BaseURL)
	type outcome struct {
		port detectedPort
		err  error
	}
	jobs := make(chan detectedPort)
	results := make(chan outcome, len(detected))
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for port := range jobs {
				results <- outcome{port, probePreviewHTTP(ctx, engine, env, port.Port, base.Host)}
			}
		}()
	}
	for _, port := range detected {
		if port.Port > 0 && port.Port <= 65535 && !port.Protected {
			jobs <- port
		}
	}
	close(jobs)
	workers.Wait()
	close(results)
	result := serviceDiscovery{Services: []discoveredService{}}
	for found := range results {
		if found.err == nil {
			result.Services = append(result.Services, discoveredService{found.port, "ready"})
		} else if !definitelyNotHTTP(found.err) {
			result.UncertainPorts = append(result.UncertainPorts, found.port)
			// A timeout is not evidence of a stopped service. Only previously
			// confirmed HTTP listeners are retained as uncertain rows. Unknown
			// TCP services can close HTTP probes (for example Redis); those do
			// not make the webpage list unhealthy without prior HTTP evidence.
			if known[found.port.Port] {
				result.Uncertain = true
				result.Services = append(result.Services, discoveredService{found.port, "unknown"})
			}
		}
	}
	return result, nil
}

func definitelyNotHTTP(err error) bool {
	// net/http only emits these after receiving an invalid HTTP status line;
	// transport failures/EOF/timeouts remain uncertain, never treated as stops.
	message := err.Error()
	return strings.Contains(message, "malformed HTTP response") || strings.Contains(message, "malformed HTTP status code") || strings.Contains(message, "malformed HTTP version") || strings.Contains(message, "server gave HTTP response to HTTPS client")
}
