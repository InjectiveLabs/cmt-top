package divergence

import "time"

// VoteType selects prevote / precommit. Mirrors cmtproto.SignedMsgType but
// without the cometbft import in this package, keeping the tracker pure.
type VoteType uint8

const (
	Prevote   VoteType = 1
	Precommit VoteType = 2
)

func (v VoteType) String() string {
	switch v {
	case Prevote:
		return "prevote"
	case Precommit:
		return "precommit"
	}
	return "unknown"
}

// VoteEvent is the input to the tracker.
type VoteEvent struct {
	Height        int64
	Round         int64
	Type          VoteType
	ValidatorAddr string // hex (uppercase per cometbft convention)
	BlockIDHash   string // lowercase hex; "" for nil-vote
	Timestamp     time.Time
}

// ValidatorPower lets the tracker weight votes.
type ValidatorPower struct {
	Address string // hex
	Power   int64
	Moniker string
}

// Group is one BlockID-hash group.
type Group struct {
	BlockIDHash       string  // "" for nil-vote bucket
	VotingPower       int64
	VotingPowerPct    float64
	ValidatorCount    int
	SampleMonikers    []string // up to 5
	Validators        []string // hex addrs (full set)
	IsCanonical       bool     // resolved post-commit
}

// RoundReport is one (height, round, type) report.
type RoundReport struct {
	Height           int64
	Round            int64
	Type             VoteType
	Groups           []Group // sorted desc by voting power
	TotalVotingPower int64
	TotalVotedPower  int64 // sum across groups (excludes "absent")
	Resolved         bool
	CanonicalHash    string
	IsDivergent      bool // any non-leader group above threshold
}

// Report is what the tracker publishes — current snapshot + recent history.
type Report struct {
	Live    []RoundReport
	History []RoundReport
}
