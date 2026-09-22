package web

import (
	"encoding/json"
	"io"
	"strconv"
	"time"
)

// wsWire owns immutable, validated JSON. Only the small sequence suffix changes
// per recipient. In particular, never pass payload back through json.Marshal:
// even RawMessage would scan and copy it for every viewer.
type wsWire struct{ prefix, payload []byte }

func (h *wsHub) prepare(env wsEnvelope) (wsEnvelope, error) {
	if env.wire != nil {
		return env, nil
	}
	started := time.Now()
	if env.Ts == "" {
		env.Ts = started.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(env.Payload)
	if err != nil {
		return env, err
	}
	head, err := json.Marshal(struct {
		Type   string `json:"type"`
		Ts     string `json:"ts"`
		Height int64  `json:"height,omitempty"`
		Round  int64  `json:"round,omitempty"`
	}{env.Type, env.Ts, env.Height, env.Round})
	if err != nil {
		return env, err
	}
	env.wire = &wsWire{prefix: append(head[:len(head)-1], []byte(`,"payload":`)...), payload: payload}
	env.size = len(env.wire.prefix) + len(payload) + 32 // includes largest uint64 sequence
	h.record("encode_seconds", time.Since(started).Seconds())
	h.record("payload_encoded_bytes/"+env.Type, float64(len(payload)))
	return env, nil
}
func (env wsEnvelope) writeTo(w io.Writer) (int, error) {
	var tail [40]byte
	suffix := append(tail[:0], `,"seq":`...)
	suffix = strconv.AppendUint(suffix, env.Seq, 10)
	suffix = append(suffix, '}')
	total := 0
	for _, part := range [][]byte{env.wire.prefix, env.wire.payload, suffix} {
		n, err := w.Write(part)
		total += n
		if err != nil {
			return total, err
		}
		if n != len(part) {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
