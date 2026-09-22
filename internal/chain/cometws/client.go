// Package cometws wraps cometbft's WS client with a state machine that
// reconnects with exponential backoff and resubscribes to all queries.
package cometws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"time"

	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/gorilla/websocket"
)

// State is the transport state.
type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateSubscribing
	StateLive
	StateReconnecting
)

func (s State) String() string {
	return [...]string{"disconnected", "connecting", "subscribing", "live", "reconnecting"}[s]
}

// Event is one decoded WS event delivered to the consumer. Data is one of our
// own tolerant types defined in eventdata.go.
type Event struct {
	Query string
	Data  EventData
}

// Handler is called for each Event. It must return quickly; long work belongs
// in a downstream goroutine.
type Handler func(Event)

// LifecycleHook fires on transport state changes.
type LifecycleHook func(state State, endpoint string, err error)

// Client is a single-connection WS subscriber with auto-reconnect.
type Client struct {
	endpoints   []string
	queries     []string
	handler     Handler
	onLifecycle LifecycleHook
	idleTimeout time.Duration
	maxBackoff  time.Duration
	logger      *slog.Logger

	mu     sync.Mutex
	state  State
	active string
}

// Options configure a Client.
type Options struct {
	Endpoints   []string
	Queries     []string
	Handler     Handler
	OnLifecycle LifecycleHook
	IdleTimeout time.Duration
	MaxBackoff  time.Duration
	Logger      *slog.Logger
}

func New(opts Options) (*Client, error) {
	if len(opts.Endpoints) == 0 {
		return nil, errors.New("no endpoints")
	}
	if opts.Handler == nil {
		return nil, errors.New("nil handler")
	}
	if opts.IdleTimeout < 0 || opts.MaxBackoff < 0 {
		return nil, errors.New("timeouts must not be negative")
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Client{
		endpoints:   opts.Endpoints,
		queries:     opts.Queries,
		handler:     opts.Handler,
		onLifecycle: opts.OnLifecycle,
		idleTimeout: opts.IdleTimeout,
		maxBackoff:  opts.MaxBackoff,
		logger:      opts.Logger,
	}, nil
}

// Run blocks until ctx is cancelled, reconnecting forever on failure.
func (c *Client) Run(ctx context.Context) {
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ep := c.endpoints[attempt%len(c.endpoints)]
		err := c.connectAndPump(ctx, ep)
		if errors.Is(err, context.Canceled) {
			return
		}
		if c.State() == StateLive {
			attempt = 0
		}
		c.setState(StateReconnecting, ep, err)
		c.logger.Warn("ws disconnected", "endpoint", ep, "err", err)

		backoff := time.Duration(250*(1<<min(attempt, 7))) * time.Millisecond
		if backoff > c.maxBackoff {
			backoff = c.maxBackoff
		}
		jitter := time.Duration(rand.Int63n(max(1, int64(backoff)/5)))
		wait := backoff + jitter
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		attempt++
	}
}

// Active returns the currently-connected endpoint URL.
func (c *Client) Active() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// State returns the current transport state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) setState(s State, ep string, err error) {
	c.mu.Lock()
	c.state = s
	c.active = ep
	c.mu.Unlock()
	if c.onLifecycle != nil {
		c.onLifecycle(s, ep, err)
	}
}

func (c *Client) connectAndPump(ctx context.Context, endpoint string) error {
	c.setState(StateConnecting, endpoint, nil)

	wsURL, err := subscriptionURL(endpoint)
	if err != nil {
		return err
	}
	// The wrapper owns reconnect and resubscription. The CometBFT WS client
	// reconnects internally without restoring our subscriptions and can race
	// Stop against starting replacement reader/writer goroutines.
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	conn, resp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	conn.SetReadLimit(64 << 20)

	c.setState(StateSubscribing, endpoint, nil)
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	for i, q := range c.queries {
		request := struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      int               `json:"id"`
			Method  string            `json:"method"`
			Params  map[string]string `json:"params"`
		}{"2.0", i, "subscribe", map[string]string{"query": q}}
		if err := conn.WriteJSON(request); err != nil {
			return fmt.Errorf("subscribe %q: %w", q, err)
		}
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(c.idleTimeout))
		var resp rpctypes.RPCResponse
		if err := conn.ReadJSON(&resp); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ws read: %w", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if resp.Error != nil {
			return fmt.Errorf("ws subscription: %s", resp.Error)
		}
		if len(resp.Result) == 0 {
			continue // ack to subscribe
		}
		ev, err := decodeEvent(resp.Result)
		if err != nil {
			c.logger.Debug("ws decode", "err", err)
			continue
		}
		if ev.Data == nil {
			continue
		}
		if c.State() != StateLive {
			c.setState(StateLive, endpoint, nil)
			c.logger.Info("ws live", "endpoint", endpoint, "subs", len(c.queries))
		}
		c.handler(ev)
	}
}

// decodeEvent unmarshals a tendermint result event with a typed payload.
func decodeEvent(raw json.RawMessage) (Event, error) {
	var env struct {
		Query string          `json:"query"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Event{}, err
	}
	if len(env.Data) == 0 {
		return Event{Query: env.Query}, nil
	}
	var typed struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(env.Data, &typed); err != nil {
		return Event{}, err
	}
	d, err := unmarshalEventData(typed.Type, typed.Value)
	if err != nil {
		return Event{}, fmt.Errorf("event %s: %w", typed.Type, err)
	}
	return Event{Query: env.Query, Data: d}, nil
}

func unmarshalEventData(t string, v json.RawMessage) (EventData, error) {
	switch t {
	case "tendermint/event/NewBlock", "cometbft/event/NewBlock":
		var d EventDataNewBlock
		return d, json.Unmarshal(v, &d)
	case "tendermint/event/NewRound", "cometbft/event/NewRound":
		var d EventDataNewRound
		return d, json.Unmarshal(v, &d)
	case "tendermint/event/Vote", "cometbft/event/Vote":
		var d EventDataVote
		return d, json.Unmarshal(v, &d)
	case "tendermint/event/ValidatorSetUpdates", "cometbft/event/ValidatorSetUpdates":
		var d EventDataValidatorSetUpdates
		return d, json.Unmarshal(v, &d)
	default:
		return nil, fmt.Errorf("unknown event type %q", t)
	}
}

func subscriptionURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid websocket endpoint")
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", errors.New("websocket endpoint must use http, https, ws or wss")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/websocket"
	u.RawPath = ""
	u.Fragment = ""
	return u.String(), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
