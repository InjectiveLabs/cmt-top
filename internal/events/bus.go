// Package events is a small in-process pub/sub bus.
//
// Design:
//   - One publisher (the orchestrator), many subscribers (TUI, web, divergence
//     tracker, prometheus).
//   - Subscribers consume from buffered channels. If a subscriber is slow, the
//     bus drops the oldest event and increments a per-subscriber counter rather
//     than block the publisher.
//   - Events are typed via the Kind enum + a payload any. Subscribers filter
//     by kind at subscription time.
package events

import (
	"sync"
	"sync/atomic"
)

type Kind uint16

const (
	KindNewBlock Kind = iota + 1
	KindNewBlockHeader
	KindRoundChanged
	KindVoteReceived
	KindValidatorSetUpdated
	KindStatusUpdated
	KindUpgradePlanUpdated
	KindBlockTimeUpdated
	KindDivergenceDetected
	KindDivergenceResolved
	KindEndpointAppHashDivergence
	KindConnectionLost
	KindConnectionRestored
	KindError
	KindLog
)

func (k Kind) String() string {
	switch k {
	case KindNewBlock:
		return "new_block"
	case KindNewBlockHeader:
		return "new_block_header"
	case KindRoundChanged:
		return "round_changed"
	case KindVoteReceived:
		return "vote_received"
	case KindValidatorSetUpdated:
		return "validator_set_updated"
	case KindStatusUpdated:
		return "status_updated"
	case KindUpgradePlanUpdated:
		return "upgrade_plan_updated"
	case KindBlockTimeUpdated:
		return "block_time_updated"
	case KindDivergenceDetected:
		return "divergence_detected"
	case KindDivergenceResolved:
		return "divergence_resolved"
	case KindEndpointAppHashDivergence:
		return "endpoint_apphash_divergence"
	case KindConnectionLost:
		return "connection_lost"
	case KindConnectionRestored:
		return "connection_restored"
	case KindError:
		return "error"
	case KindLog:
		return "log"
	}
	return "unknown"
}

// Event is the envelope on the bus.
type Event struct {
	Kind    Kind
	Payload any
}

type subscription struct {
	ch     chan Event
	kinds  map[Kind]struct{}
	dropped atomic.Uint64
	name   string
}

// Bus is the central pub/sub.
type Bus struct {
	mu     sync.RWMutex
	subs   map[uint64]*subscription
	nextID uint64
	bufCap int
}

func NewBus(bufCap int) *Bus {
	if bufCap <= 0 {
		bufCap = 256
	}
	return &Bus{
		subs:   make(map[uint64]*subscription),
		bufCap: bufCap,
	}
}

// Subscribe returns a channel that will receive events of the given kinds. If
// kinds is empty, the subscriber receives every kind. The cancel function
// removes the subscription and closes the channel. name is used for metrics.
func (b *Bus) Subscribe(name string, kinds ...Kind) (<-chan Event, func()) {
	sub := &subscription{
		ch:    make(chan Event, b.bufCap),
		kinds: make(map[Kind]struct{}, len(kinds)),
		name:  name,
	}
	for _, k := range kinds {
		sub.kinds[k] = struct{}{}
	}

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = sub
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if s, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(s.ch)
		}
		b.mu.Unlock()
	}
	return sub.ch, cancel
}

// Publish fans out to every matching subscriber. Non-blocking: if a subscriber's
// buffer is full, the oldest event is dropped (with a counter increment).
func (b *Bus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if len(sub.kinds) > 0 {
			if _, ok := sub.kinds[ev.Kind]; !ok {
				continue
			}
		}
		select {
		case sub.ch <- ev:
		default:
			// Drop oldest, push new.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- ev:
				sub.dropped.Add(1)
			default:
				sub.dropped.Add(1)
			}
		}
	}
}

// Stats returns dropped-event counts per subscriber.
func (b *Bus) Stats() map[string]uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]uint64, len(b.subs))
	for _, s := range b.subs {
		out[s.name] = s.dropped.Load()
	}
	return out
}
