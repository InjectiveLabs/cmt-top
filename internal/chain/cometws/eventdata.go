package cometws

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// EventData is a unified payload type for the events we care about. Because
// CometBFT v1.0 changed several int fields to JSON strings (height, round,
// validator_index, voting_power), we don't use the cometbft typed structs;
// instead we parse a tolerant subset.
type EventData interface{ isEventData() }

// FlexInt accepts either a JSON number or a JSON string.
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		*f = FlexInt(n)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = FlexInt(n)
	return nil
}

// VoteData mirrors a tendermint vote received over WS, but field-by-field
// tolerant against v0.38 / v1.0 differences.
type VoteData struct {
	Type             int       `json:"type"`
	Height           FlexInt   `json:"height"`
	Round            FlexInt   `json:"round"`
	BlockID          BlockID   `json:"block_id"`
	Timestamp        time.Time `json:"timestamp"`
	ValidatorAddress string    `json:"validator_address"` // hex
	ValidatorIndex   FlexInt   `json:"validator_index"`
}

type BlockID struct {
	Hash          string         `json:"hash"`
	PartSetHeader PartSetHeader  `json:"parts"`
}

type PartSetHeader struct {
	Total FlexInt `json:"total"`
	Hash  string  `json:"hash"`
}

// EventDataVote — orchestrator handler matches on this.
type EventDataVote struct {
	Vote VoteData `json:"Vote"`
}

func (EventDataVote) isEventData() {}

// EventDataNewBlock has a Block subtree; we only pull what we need.
type EventDataNewBlock struct {
	Block struct {
		Header struct {
			Height          FlexInt   `json:"height"`
			Time            time.Time `json:"time"`
			ProposerAddress string    `json:"proposer_address"`
			AppHash         string    `json:"app_hash"`
			DataHash        string    `json:"data_hash"`
		} `json:"header"`
		Data struct {
			Txs []json.RawMessage `json:"txs"`
		} `json:"data"`
	} `json:"block"`
	BlockID BlockID `json:"block_id"`
}

func (EventDataNewBlock) isEventData() {}

type EventDataNewRound struct {
	Height FlexInt `json:"height"`
	Round  FlexInt `json:"round"`
	Step   string  `json:"step"`
	Proposer struct {
		Address string  `json:"address"`
		Index   FlexInt `json:"index"`
	} `json:"proposer"`
}

func (EventDataNewRound) isEventData() {}

type EventDataValidatorSetUpdates struct {
	ValidatorUpdates []json.RawMessage `json:"validator_updates"`
}

func (EventDataValidatorSetUpdates) isEventData() {}

// VoteType wire constants. CometBFT historically used proto SignedMsgType
// (1=prevote, 2=precommit). v1.0 may emit them as strings; the parser above
// handles either.
const (
	VoteTypePrevote   = 1
	VoteTypePrecommit = 2
)
