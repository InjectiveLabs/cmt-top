// Package cometrpc wraps cometbft's typed JSON-RPC HTTP client with multi-RPC
// failover and a per-endpoint circuit breaker.
package cometrpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/sony/gobreaker"
)

// Endpoint is one configured RPC URL.
type Endpoint struct {
	URL     string
	Primary bool
}

// Pool is a multi-endpoint RPC client. Use the typed methods which dispatch to
// whichever endpoint is currently healthy.
type Pool struct {
	mu        sync.RWMutex
	endpoints []*backend
	active    atomic.Int32 // index into endpoints
}

type backend struct {
	endpoint Endpoint
	client   *http.HTTP
	cb       *gobreaker.CircuitBreaker
}

// NewPool builds a Pool from a list of Endpoints. Primary endpoints are tried
// first.
func NewPool(eps []Endpoint) (*Pool, error) {
	if len(eps) == 0 {
		return nil, errors.New("no endpoints")
	}
	// stable sort: primary first
	sorted := make([]Endpoint, 0, len(eps))
	for _, e := range eps {
		if e.Primary {
			sorted = append(sorted, e)
		}
	}
	for _, e := range eps {
		if !e.Primary {
			sorted = append(sorted, e)
		}
	}
	p := &Pool{endpoints: make([]*backend, 0, len(sorted))}
	for _, e := range sorted {
		c, err := http.New(e.URL, "/websocket")
		if err != nil {
			return nil, fmt.Errorf("init %s: %w", e.URL, err)
		}
		cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    e.URL,
			Timeout: 30 * time.Second,
			ReadyToTrip: func(c gobreaker.Counts) bool {
				return c.ConsecutiveFailures >= 5
			},
		})
		p.endpoints = append(p.endpoints, &backend{endpoint: e, client: c, cb: cb})
	}
	return p, nil
}

// Active returns the URL currently in use.
func (p *Pool) Active() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	idx := int(p.active.Load())
	if idx < 0 || idx >= len(p.endpoints) {
		return ""
	}
	return p.endpoints[idx].endpoint.URL
}

// SetActive sets the active endpoint to the given URL (used by the WS client to
// keep HTTP+WS pinned to the same endpoint).
func (p *Pool) SetActive(url string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i, b := range p.endpoints {
		if b.endpoint.URL == url {
			p.active.Store(int32(i))
			return
		}
	}
}

// do executes fn against the active endpoint, falling back to others on
// failure. Returns the URL that succeeded plus the result.
func do[T any](p *Pool, fn func(*http.HTTP) (T, error)) (T, string, error) {
	var zero T
	p.mu.RLock()
	endpoints := append([]*backend(nil), p.endpoints...)
	startIdx := int(p.active.Load())
	p.mu.RUnlock()

	var lastErr error
	n := len(endpoints)
	for off := 0; off < n; off++ {
		i := (startIdx + off) % n
		b := endpoints[i]
		res, err := b.cb.Execute(func() (interface{}, error) {
			return fn(b.client)
		})
		if err == nil {
			p.active.Store(int32(i))
			return res.(T), b.endpoint.URL, nil
		}
		lastErr = err
	}
	return zero, "", fmt.Errorf("all endpoints failed: %w", lastErr)
}

// Status fetches /status from the active endpoint.
func (p *Pool) Status(ctx context.Context) (*coretypes.ResultStatus, string, error) {
	return do(p, func(c *http.HTTP) (*coretypes.ResultStatus, error) {
		return c.Status(ctx)
	})
}

// Validators fetches one page of the validator set at the given height (nil =
// latest).
func (p *Pool) Validators(ctx context.Context, height *int64, page, perPage int) (*coretypes.ResultValidators, string, error) {
	return do(p, func(c *http.HTTP) (*coretypes.ResultValidators, error) {
		return c.Validators(ctx, height, &page, &perPage)
	})
}

// AllValidators iterates pages until the full set is collected.
func (p *Pool) AllValidators(ctx context.Context, height *int64) ([]*cmttypes.Validator, int64, string, error) {
	const perPage = 100
	page := 1
	all := []*cmttypes.Validator{}
	var lastURL string
	for {
		res, url, err := p.Validators(ctx, height, page, perPage)
		if err != nil {
			return nil, 0, "", err
		}
		lastURL = url
		all = append(all, res.Validators...)
		if len(all) >= res.Total || len(res.Validators) == 0 {
			return all, int64(res.BlockHeight), lastURL, nil
		}
		page++
	}
}

// Block fetches a block by height (nil = latest).
func (p *Pool) Block(ctx context.Context, height *int64) (*coretypes.ResultBlock, string, error) {
	return do(p, func(c *http.HTTP) (*coretypes.ResultBlock, error) {
		return c.Block(ctx, height)
	})
}

// BlockResults fetches /block_results at the given height.
func (p *Pool) BlockResults(ctx context.Context, height *int64) (*coretypes.ResultBlockResults, string, error) {
	return do(p, func(c *http.HTTP) (*coretypes.ResultBlockResults, error) {
		return c.BlockResults(ctx, height)
	})
}

// DumpConsensusState fetches the full consensus state (used as a snapshot
// fallback when WS isn't connected, and to seed the divergence tracker on
// startup).
func (p *Pool) DumpConsensusState(ctx context.Context) (*coretypes.ResultDumpConsensusState, string, error) {
	return do(p, func(c *http.HTTP) (*coretypes.ResultDumpConsensusState, error) {
		return c.DumpConsensusState(ctx)
	})
}

// ConsensusState fetches the compact consensus_state response.
func (p *Pool) ConsensusState(ctx context.Context) (*coretypes.ResultConsensusState, string, error) {
	return do(p, func(c *http.HTTP) (*coretypes.ResultConsensusState, error) {
		return c.ConsensusState(ctx)
	})
}
