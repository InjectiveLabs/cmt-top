// Package obs configures structured logging and an in-memory ring buffer that
// the TUI debug pane and the web log.debug channel both read from.
package obs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Format selects the slog handler.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Setup configures the default slog logger and returns the shared ring buffer.
func Setup(level slog.Level, format Format, out io.Writer) *Ring {
	if out == nil {
		out = os.Stderr
	}
	ring := NewRing(512)

	var primary slog.Handler
	opts := &slog.HandlerOptions{Level: level}
	switch format {
	case FormatJSON:
		primary = slog.NewJSONHandler(out, opts)
	default:
		primary = slog.NewTextHandler(out, opts)
	}

	tee := &teeHandler{primary: primary, ring: ring, level: level}
	slog.SetDefault(slog.New(tee))
	return ring
}

type teeHandler struct {
	primary slog.Handler
	ring    *Ring
	level   slog.Level
}

func (h *teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	h.ring.Push(LogLine{
		Time:  r.Time,
		Level: r.Level.String(),
		Msg:   r.Message,
		Attrs: collectAttrs(r),
	})
	return h.primary.Handle(ctx, r)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{primary: h.primary.WithAttrs(attrs), ring: h.ring, level: h.level}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{primary: h.primary.WithGroup(name), ring: h.ring, level: h.level}
}

func collectAttrs(r slog.Record) map[string]string {
	out := make(map[string]string, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	return out
}

// LogLine is one entry in the ring.
type LogLine struct {
	Time  time.Time
	Level string
	Msg   string
	Attrs map[string]string
}

// Ring is a fixed-size lock-protected ring of LogLines.
type Ring struct {
	mu       sync.RWMutex
	buf      []LogLine
	head     int
	full     bool
	subs     map[chan<- LogLine]struct{}
}

func NewRing(cap int) *Ring {
	if cap <= 0 {
		cap = 256
	}
	return &Ring{
		buf:  make([]LogLine, cap),
		subs: make(map[chan<- LogLine]struct{}),
	}
}

func (r *Ring) Push(l LogLine) {
	r.mu.Lock()
	r.buf[r.head] = l
	r.head = (r.head + 1) % len(r.buf)
	if r.head == 0 {
		r.full = true
	}
	for ch := range r.subs {
		select {
		case ch <- l:
		default:
		}
	}
	r.mu.Unlock()
}

// Snapshot returns the lines in chronological order.
func (r *Ring) Snapshot() []LogLine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.full {
		out := make([]LogLine, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]LogLine, len(r.buf))
	n := copy(out, r.buf[r.head:])
	copy(out[n:], r.buf[:r.head])
	return out
}

// Subscribe streams new log lines to ch. ch must be buffered.
func (r *Ring) Subscribe(ch chan<- LogLine) (cancel func()) {
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// Recover logs a panic with a stack trace and returns. Use as a deferred call
// at the top of every long-running goroutine.
func Recover(name string) {
	if rec := recover(); rec != nil {
		slog.Error("panic", "where", name, "recovered", fmt.Sprint(rec))
	}
}
