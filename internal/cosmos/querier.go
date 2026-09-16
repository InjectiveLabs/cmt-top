// Package cosmos enriches consensus validators with Cosmos staking metadata
// (moniker, operator address, jail status, commission) via the LCD REST API.
//
// We deliberately do NOT depend on cosmos-sdk here. LCD JSON is stable enough,
// and avoiding the SDK keeps our binary small and our build matrix sane.
package cosmos

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/state"
)

// Querier resolves consensus addresses (hex) into ChainValidators.
type Querier struct {
	lcdURL       string
	bech32Prefix string
	httpc        *http.Client
}

// Options configures a Querier.
type Options struct {
	LCDURL       string
	Bech32Prefix string
	Timeout      time.Duration
}

func New(opts Options) *Querier {
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	return &Querier{
		lcdURL:       strings.TrimRight(opts.LCDURL, "/"),
		bech32Prefix: opts.Bech32Prefix,
		httpc:        &http.Client{Timeout: opts.Timeout},
	}
}

// Validators fetches all (active + inactive) validators from x/staking via the
// LCD REST endpoint. Returns ChainValidators keyed by hex consensus address.
func (q *Querier) Validators(ctx context.Context) ([]state.ChainValidator, error) {
	if q.lcdURL == "" {
		return nil, errors.New("LCD URL not configured")
	}
	out := []state.ChainValidator{}
	pageKey := ""
	for {
		u := fmt.Sprintf("%s/cosmos/staking/v1beta1/validators?pagination.limit=200", q.lcdURL)
		if pageKey != "" {
			u += "&pagination.key=" + url.QueryEscape(pageKey)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := q.httpc.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("LCD %s: %s", u, resp.Status)
		}
		var pr lcdValidatorsResp
		if err := json.Unmarshal(body, &pr); err != nil {
			return nil, fmt.Errorf("decode validators: %w", err)
		}
		for _, v := range pr.Validators {
			cv, err := q.toChainValidator(v)
			if err != nil {
				continue
			}
			out = append(out, cv)
		}
		if pr.Pagination.NextKey == "" || len(pr.Validators) == 0 {
			return out, nil
		}
		pageKey = pr.Pagination.NextKey
	}
}

// UpgradePlan returns the current upgrade plan, or nil if none scheduled.
func (q *Querier) UpgradePlan(ctx context.Context) (*state.Upgrade, error) {
	if q.lcdURL == "" {
		return nil, errors.New("LCD URL not configured")
	}
	u := q.lcdURL + "/cosmos/upgrade/v1beta1/current_plan"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := q.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("LCD %s: %s", u, resp.Status)
	}
	var pr struct {
		Plan *struct {
			Name   string `json:"name"`
			Height string `json:"height"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, err
	}
	if pr.Plan == nil {
		return nil, nil
	}
	height, _ := parseInt(pr.Plan.Height)
	return &state.Upgrade{Name: pr.Plan.Name, Height: height}, nil
}

type lcdValidatorsResp struct {
	Validators []lcdValidator `json:"validators"`
	Pagination struct {
		NextKey string `json:"next_key"`
	} `json:"pagination"`
}

type lcdValidator struct {
	OperatorAddress string `json:"operator_address"`
	ConsensusPubkey struct {
		Type string `json:"@type"`
		Key  string `json:"key"`
	} `json:"consensus_pubkey"`
	Jailed      bool `json:"jailed"`
	Status      string `json:"status"`
	Description struct {
		Moniker string `json:"moniker"`
	} `json:"description"`
	Commission struct {
		CommissionRates struct {
			Rate string `json:"rate"`
		} `json:"commission_rates"`
	} `json:"commission"`
}

func (q *Querier) toChainValidator(v lcdValidator) (state.ChainValidator, error) {
	pubkey, err := base64.StdEncoding.DecodeString(v.ConsensusPubkey.Key)
	if err != nil {
		return state.ChainValidator{}, err
	}
	// CometBFT consensus address = first 20 bytes of SHA256(ed25519 pubkey).
	sum := sha256.Sum256(pubkey)
	consAddr := hex.EncodeToString(sum[:20])
	consAddrHex := strings.ToUpper(consAddr)
	return state.ChainValidator{
		OperatorAddress: v.OperatorAddress,
		RawConsAddr:     consAddrHex,
		Moniker:         v.Description.Moniker,
		Jailed:          v.Jailed,
		Active:          v.Status == "BOND_STATUS_BONDED",
		CommissionRate:  v.Commission.CommissionRates.Rate,
	}, nil
}

func parseInt(s string) (int64, error) {
	var x int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not int: %s", s)
		}
		x = x*10 + int64(c-'0')
	}
	return x, nil
}
