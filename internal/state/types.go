package state

import (
	"math/big"
	"time"
)

// VoteKind classifies a single vote received from a validator. The classic tmtop
// tristate, retained for display compatibility.
type VoteKind uint8

const (
	VoteAbsent VoteKind = iota // no vote received yet
	VoteForBlock
	VoteNil  // explicit nil-Vote
	VoteZero // SIGNED_MSG_TYPE_PREVOTE(...) 000000000000 — historic "zero" placeholder
)

func (k VoteKind) String() string {
	switch k {
	case VoteForBlock:
		return "voted"
	case VoteNil:
		return "nil"
	case VoteZero:
		return "zero"
	default:
		return "absent"
	}
}

// Vote retains the BlockID hash so the divergence tracker can group validators
// by what they actually voted for. tmtop's pkg/types/vote.go discarded this.
type Vote struct {
	Kind        VoteKind
	BlockIDHash string // lowercase hex; "" for absent/nil/zero
	Timestamp   time.Time
}

// RoundVote is one validator's votes in one round.
type RoundVote struct {
	Address    string // consensus address (hex)
	Prevote    Vote
	Precommit  Vote
	IsProposer bool
}

// Validator is the bare consensus identity.
type Validator struct {
	Address            string
	Index              int
	VotingPower        *big.Int
	VotingPowerPercent float64
}

// ChainValidator is the staking-module enrichment.
type ChainValidator struct {
	OperatorAddress string
	ConsensusAddr   string // bech32, configurable prefix
	RawConsAddr     string // hex
	Moniker         string
	Jailed          bool
	Active          bool
	CommissionRate  string
	AssignedAddress string // ICS or Injective key-rotation, optional
}

// ValidatorWithVote pairs identity + this round's votes + chain-level enrichment.
type ValidatorWithVote struct {
	Validator      Validator
	RoundVote      RoundVote
	ChainValidator *ChainValidator
}

// AllRoundsView is the per-round vote matrix shown in tmtop's "all rounds" mode.
type AllRoundsView struct {
	Validators []Validator
	// RoundsVotes[round][validatorIndex] = RoundVote
	RoundsVotes [][]RoundVote
}

// Upgrade plan from x/upgrade.
type Upgrade struct {
	Name   string
	Height int64
}

// NodeStatus is the subset of /status we display.
type NodeStatus struct {
	Network       string
	CometVersion  string
	AppVersion    string
	Moniker       string
	OurValidator  string // hex consensus address of the node we're connected to
	CatchingUp    bool
	LatestHeight  int64
	LatestBlockTs time.Time
}

// StateData is the monitoring state. Read through Snapshot and update only
// inside Mutate; no pointers into live state should escape its lock.
type StateData struct {
	// Consensus
	Height    int64
	Round     int64
	Step      int64
	StartTime time.Time

	// Round views
	LastRound *RoundView
	AllRounds *AllRoundsView

	// Chain
	ChainValidators []ChainValidator
	NodeStatus      *NodeStatus
	Upgrade         *Upgrade
	BlockTime       time.Duration
	Blocks          []BlockSample
	Health          Health
	RPCComparison   RPCComparison
	ValidatorHeight int64

	// Errors per source
	ConsensusError  error
	ValidatorsError error
	StatusError     error
	UpgradeError    error

	// LastCommittedHeight is the highest height we've seen NewBlock for. Votes
	// with Height <= LastCommittedHeight are stragglers from a closed height
	// and must not pollute the live round display.
	LastCommittedHeight int64

	// Connection
	ActiveRPC   string
	WSConnected bool
	LastUpdate  time.Time
}

// Health describes the upstream connection, independently of a browser's feed.
// Timestamps record successful observations, never failed polling attempts.
type Health struct {
	Mode          string    `json:"mode"`
	WSConnected   bool      `json:"wsConnected"`
	WSEndpoint    string    `json:"wsEndpoint"`
	HTTPEndpoint  string    `json:"httpEndpoint"`
	LastEventAt   time.Time `json:"lastEventAt,omitzero"`
	LastHTTPAt    time.Time `json:"lastHTTPAt,omitzero"`
	LastSuccessAt time.Time `json:"lastSuccessAt,omitzero"`
	LastError     string    `json:"lastError,omitempty"`
	StaleAfterMs  int64     `json:"staleAfterMs"`
}

const BlockHistoryLimit = 120

type BlockSample struct {
	Height      int64     `json:"height"`
	Time        time.Time `json:"time"`
	BlockIDHash string    `json:"blockIDHash"`
	AppHash     string    `json:"appHash"`
	NumTxs      int       `json:"numTxs"`
	BlockTimeMs int64     `json:"blockTimeMs"`
}

// RPCComparison compares the AppHash in block headers at exactly Height.
// Lagging/incompatible/unreachable endpoints never count as a hash mismatch.
type RPCComparison struct {
	Status    string           `json:"status"`
	Height    int64            `json:"height"`
	ChainID   string           `json:"chainId"`
	CheckedAt time.Time        `json:"checkedAt,omitzero"`
	Endpoints []EndpointResult `json:"endpoints"`
}

type EndpointResult struct {
	Endpoint     string `json:"endpoint"`
	Status       string `json:"status"`
	ChainID      string `json:"chainId"`
	Height       int64  `json:"height"`
	LatestHeight int64  `json:"latestHeight"`
	AppHash      string `json:"appHash"`
	Error        string `json:"error,omitempty"`
}

// RoundView is the latest round's votes used by both the validator table and
// the divergence panel.
type RoundView struct {
	Round      int64
	Validators []ValidatorWithVote
	TotalVP    *big.Int
}
