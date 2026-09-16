package core

import (
	"math"
	"math/big"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/state"
)

// advanceRound is shared by all consensus inputs. Call under State.Mutate.
// Height zero means no active consensus context has been observed yet.
func advanceRound(s *state.StateData, height, round, step int64) bool {
	if height <= s.LastCommittedHeight || height < s.Height || round < 0 || round > math.MaxInt32 {
		return false
	}
	if height == s.Height && round < s.Round {
		return false
	}
	if height != s.Height || round != s.Round {
		clearRoundVotes(s.LastRound)
		s.Height, s.Round, s.Step = height, round, step
		s.StartTime = time.Now().UTC()
		if s.LastRound != nil {
			s.LastRound.Round = round
		}
	} else if step > s.Step {
		s.Step = step
	}
	return true
}

// mergeValidatorRound retains observations by identity, never by display index.
func mergeValidatorRound(s *state.StateData, rows []state.ValidatorWithVote, total *big.Int, height int64) bool {
	if height < s.ValidatorHeight {
		return false
	}
	previous := map[string]state.RoundVote{}
	if s.LastRound != nil && s.LastRound.Round == s.Round {
		for _, row := range s.LastRound.Validators {
			previous[row.Validator.Address] = row.RoundVote
		}
	}
	for i := range rows {
		rows[i].RoundVote = previous[rows[i].Validator.Address]
		rows[i].RoundVote.Address = rows[i].Validator.Address
	}
	s.LastRound = &state.RoundView{Round: s.Round, Validators: rows, TotalVP: total}
	s.ValidatorHeight = height
	return true
}

func appendBlock(s *state.StateData, sample state.BlockSample) {
	if n := len(s.Blocks); n > 0 {
		last := s.Blocks[n-1]
		if sample.Height <= last.Height {
			return
		}
		if sample.Height == last.Height+1 && sample.Time.After(last.Time) {
			sample.BlockTimeMs = sample.Time.Sub(last.Time).Milliseconds()
		}
	}
	s.Blocks = append(s.Blocks, sample)
	if len(s.Blocks) > state.BlockHistoryLimit {
		copy(s.Blocks, s.Blocks[len(s.Blocks)-state.BlockHistoryLimit:])
		s.Blocks = s.Blocks[:state.BlockHistoryLimit]
	}
}

func recordHTTPSuccess(s *state.StateData, endpoint string) {
	now := time.Now().UTC()
	s.ActiveRPC = endpoint
	s.Health.HTTPEndpoint = endpoint
	s.Health.LastHTTPAt = now
	s.Health.LastSuccessAt = now
	if !s.Health.WSConnected {
		s.Health.LastError = ""
	}
}
