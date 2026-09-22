package web

import "time"

// Commands are tiny. Bound aggregate parsing/queue work as well as individual
// frame size; the initial burst accommodates subscription changes and tests.
const wsCommandBurst = 512
const wsCommandsPerSecond = 100

type wsCommandBudget struct {
	tokens float64
	at     time.Time
}

func newWSCommandBudget(now time.Time) wsCommandBudget {
	return wsCommandBudget{tokens: wsCommandBurst, at: now}
}
func (b *wsCommandBudget) allow(now time.Time) bool {
	b.tokens += now.Sub(b.at).Seconds() * wsCommandsPerSecond
	if b.tokens > wsCommandBurst {
		b.tokens = wsCommandBurst
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
