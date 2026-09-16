package core

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func (o *Orchestrator) refreshRPCComparison(ctx context.Context) {
	urls := []string{o.cfg.PrimaryRPC()}
	seen := map[string]bool{urls[0]: true}
	for _, endpoint := range o.cfg.Chain.MonitoredRPCs {
		if !seen[endpoint] {
			urls = append(urls, endpoint)
			seen[endpoint] = true
		}
	}
	report := state.RPCComparison{Status: "incomplete", Endpoints: make([]state.EndpointResult, len(urls))}
	var wg sync.WaitGroup
	for i, endpoint := range urls {
		wg.Add(1)
		go func(i int, endpoint string) {
			defer wg.Done()
			row := state.EndpointResult{Endpoint: endpoint, Status: "error"}
			status, _, err := o.comparisonPools[endpoint].Status(ctx)
			if err != nil {
				row.Error = err.Error()
			} else {
				row.ChainID = status.NodeInfo.Network
				row.LatestHeight = status.SyncInfo.LatestBlockHeight
				row.Status = "pending"
			}
			report.Endpoints[i] = row
		}(i, endpoint)
	}
	wg.Wait()
	if report.Endpoints[0].Status == "pending" && report.Endpoints[0].LatestHeight > 0 {
		report.Height = report.Endpoints[0].LatestHeight
		report.ChainID = report.Endpoints[0].ChainID
		for i := range report.Endpoints {
			row := &report.Endpoints[i]
			if row.Status == "error" {
				continue
			}
			if row.ChainID != report.ChainID {
				row.Status, row.Error = "chain_mismatch", "endpoint belongs to a different chain"
				continue
			}
			if row.LatestHeight < report.Height {
				row.Status = "lagging"
				continue
			}
			wg.Add(1)
			go func(row *state.EndpointResult) {
				defer wg.Done()
				block, _, err := o.comparisonPools[row.Endpoint].Block(ctx, &report.Height)
				if err == nil && (block.Block == nil || block.Block.Height != report.Height) {
					err = fmt.Errorf("endpoint did not return requested height %d", report.Height)
				}
				if err != nil {
					row.Status, row.Error = "error", err.Error()
					return
				}
				if block.Block.ChainID != report.ChainID {
					row.Status, row.Error = "chain_mismatch", "block header belongs to a different chain"
					return
				}
				row.Height = block.Block.Height
				row.AppHash = hex.EncodeToString(block.Block.AppHash)
				row.Status = "compared"
			}(row)
		}
		wg.Wait()
		if report.Endpoints[0].Status == "compared" {
			reference := &report.Endpoints[0]
			reference.Status = "reference"
			matches, mismatch := 0, false
			for i := 1; i < len(report.Endpoints); i++ {
				row := &report.Endpoints[i]
				if row.Status != "compared" {
					continue
				}
				if row.AppHash == reference.AppHash {
					row.Status = "match"
					matches++
				} else {
					row.Status = "mismatch"
					mismatch = true
				}
			}
			if mismatch {
				report.Status = "mismatch"
			} else if len(report.Endpoints) > 1 && matches == len(report.Endpoints)-1 {
				report.Status = "matching"
			}
		} else {
			for i := 1; i < len(report.Endpoints); i++ {
				if report.Endpoints[i].Status == "compared" {
					report.Endpoints[i].Status, report.Endpoints[i].Error = "error", "reference block is unavailable"
				}
			}
		}
	} else {
		for i := range report.Endpoints {
			if report.Endpoints[i].Status == "pending" {
				report.Endpoints[i].Status, report.Endpoints[i].Error = "error", "reference height is unavailable"
			}
		}
	}
	report.CheckedAt = time.Now().UTC()
	o.state.Mutate(func(s *state.StateData) { s.RPCComparison = report })
	// The same event also carries recovery so consumers can replace stale
	// mismatch evidence with a fresh matching/incomplete result.
	o.bus.Publish(events.Event{Kind: events.KindEndpointAppHashDivergence, Payload: report})
}
