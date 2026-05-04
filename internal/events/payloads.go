package events

import (
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// NewBlock is published when a block commits.
type NewBlock struct {
	Height       int64
	BlockIDHash  string // lowercase hex
	AppHash      string // lowercase hex
	ProposerAddr string
	Time         time.Time
	NumTxs       int
}

// RoundChanged is published when (height, round, step) advances.
type RoundChanged struct {
	Height    int64
	Round     int64
	Step      int64
	StartTime time.Time
	Proposer  string // hex consensus address of this round's proposer
}

// VoteReceived is the divergence tracker's lifeblood.
type VoteReceived struct {
	Height        int64
	Round         int64
	Type          cmtproto.SignedMsgType
	ValidatorAddr string // hex consensus address
	BlockIDHash   string // lowercase hex; "" for nil-vote
	Timestamp     time.Time
}

// ValidatorSetUpdated fires on validator set changes.
type ValidatorSetUpdated struct {
	Height int64
}

// StatusUpdated fires on /status poll changes.
type StatusUpdated struct {
	Network      string
	CometVersion string
	OurValidator string
	CatchingUp   bool
	LatestHeight int64
}

// UpgradePlanUpdated fires when the upgrade plan changes.
type UpgradePlanUpdated struct {
	Name   string
	Height int64
}

// BlockTimeUpdated fires on average-block-time recalc.
type BlockTimeUpdated struct {
	BlockTime time.Duration
}

// ConnectionLost / ConnectionRestored mark transport state changes.
type ConnectionLost struct {
	Endpoint string
	Err      error
}

type ConnectionRestored struct {
	Endpoint string
}

// ErrorEvent is a structured error from any subsystem.
type ErrorEvent struct {
	Source  string
	Message string
}

// LogEvent is a debug-pane / web-debug-channel message.
type LogEvent struct {
	Level string
	Msg   string
	Time  time.Time
}
