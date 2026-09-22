package web

import "time"

const (
	wsQueueMessages = 256
	wsQueueBytes    = 4 << 20
	wsQueueMaxAge   = 2 * time.Second
)

// The client mutex owns queue contents, accounting, subscriptions and sequence.
// A wakeup is only a hint; consumers always check the queue while locked.
func (c *wsClient) queue(env wsEnvelope) {
	var err error
	env, err = c.hub.prepare(env)
	if err != nil {
		c.signalClose()
		return
	}
	c.mu.Lock()
	ok := c.queueLocked(env)
	c.mu.Unlock()
	if !ok {
		c.hub.record("slow_client", 1)
		c.signalClose()
	}
}
func (c *wsClient) queueLocked(env wsEnvelope) bool {
	if c.closed {
		return false
	}
	now := time.Now()
	limit := c.queueLimit
	if limit <= 0 {
		limit = wsQueueMessages
	}
	if env.size > wsQueueBytes || len(c.pending) > 0 && now.Sub(c.pending[0].queuedAt) > wsQueueMaxAge {
		return false
	}
	// Adjacent snapshots can safely supersede each other at the same sequence:
	// no later patch can be replayed over the newer baseline. Never replace the
	// first baseline. Other removals remain visible as sequence gaps.
	if n := len(c.pending); n > 0 && (env.Type == "state.snapshot" || env.Type == "context.snapshot") && c.pending[n-1].Type == env.Type && c.pending[n-1].Seq != 1 {
		prev := c.pending[n-1]
		if c.queuedBytes-prev.size+env.size <= wsQueueBytes {
			env.Seq, env.queuedAt = prev.Seq, prev.queuedAt
			c.pending[n-1] = env
			c.queuedBytes += env.size - prev.size
			c.hub.record("queue_bytes_delta", float64(env.size-prev.size))
			return true
		}
	}
	c.seq++
	env.Seq = c.seq
	env.queuedAt = now
	for len(c.pending) >= limit || c.queuedBytes+env.size > wsQueueBytes {
		// The initial baseline must be the first frame. If a new connection
		// cannot retain it until the writer dequeues it, close that connection
		// rather than beginning its stream with an unrecoverable patch.
		if c.pending[0].Seq == 1 && c.pending[0].Type == "state.snapshot" {
			return false
		}
		c.removeFirstLocked()
		c.dropped.Add(1)
		c.hub.recordDrop()
		if c.overloadedAt.IsZero() {
			c.overloadedAt = now
		}
		if now.Sub(c.overloadedAt) > wsQueueMaxAge {
			return false
		}
	}
	c.pending = append(c.pending, env)
	c.queuedBytes += env.size
	c.hub.record("queue_bytes_delta", float64(env.size))
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}
func (c *wsClient) removeFirstLocked() wsEnvelope {
	env := c.pending[0]
	c.pending[0] = wsEnvelope{} // release payload even if backing storage survives
	c.pending = c.pending[1:]
	c.queuedBytes -= env.size
	c.hub.record("queue_bytes_delta", -float64(env.size))
	return env
}
func (c *wsClient) dequeue() (wsEnvelope, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 || c.closed {
		return wsEnvelope{}, false
	}
	env := c.removeFirstLocked()
	if len(c.pending) < wsQueueMessages/2 && c.queuedBytes < wsQueueBytes/2 {
		c.overloadedAt = time.Time{}
	}
	return env, true
}
