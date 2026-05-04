// Package tui renders the terminal dashboard. Subscribes to the event bus and
// redraws on a throttle.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Ri-go/cmt-top/internal/divergence"
	"github.com/Ri-go/cmt-top/internal/events"
	"github.com/Ri-go/cmt-top/internal/state"
)

type UI struct {
	app     *tview.Application
	st      *state.State
	bus     *events.Bus
	tracker *divergence.Tracker
	log     *slog.Logger

	chainPane *tview.TextView
	valTable  *tview.Table
	divPane   *tview.TextView
	statusBar *tview.TextView

	timezone     *time.Location
	disableEmoji bool
	sortMode     string // "power" | "moniker" | "missrate"
	search       string
	paused       bool
}

type Options struct {
	State       *state.State
	Bus         *events.Bus
	Tracker     *divergence.Tracker
	Logger      *slog.Logger
	Timezone    string
	DisableEmoji bool
}

func New(opts Options) *UI {
	tz, _ := time.LoadLocation(opts.Timezone)
	if tz == nil {
		tz = time.UTC
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &UI{
		st:           opts.State,
		bus:          opts.Bus,
		tracker:      opts.Tracker,
		log:          opts.Logger.With("c", "tui"),
		timezone:     tz,
		disableEmoji: opts.DisableEmoji,
		sortMode:     "power",
	}
}

// Run blocks until ctx is cancelled or the user quits.
func (u *UI) Run(ctx context.Context) error {
	u.app = tview.NewApplication()
	u.chainPane = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	u.chainPane.SetBorder(true).SetTitle(" chain ")
	u.divPane = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	u.divPane.SetBorder(true).SetTitle(" apphash divergence ")
	u.valTable = tview.NewTable().SetBorders(false).SetSelectable(true, false).SetFixed(1, 0)
	u.valTable.SetBorder(true).SetTitle(" validators ")
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	flex := tview.NewFlex().
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(u.chainPane, 0, 1, false).
			AddItem(u.divPane, 0, 1, false), 0, 1, false).
		AddItem(u.valTable, 0, 2, true)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(flex, 0, 1, true).
		AddItem(u.statusBar, 1, 0, false)

	u.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape, tcell.KeyCtrlC:
			u.app.Stop()
			return nil
		}
		switch ev.Rune() {
		case 'q', 'Q':
			u.app.Stop()
			return nil
		case 'p':
			u.paused = !u.paused
		case 's':
			u.sortMode = nextSort(u.sortMode)
			u.redraw()
		case '/':
			u.promptSearch(root)
		}
		return ev
	})

	// Subscribe to bus and redraw on relevant events.
	go u.subscribeAndRedraw(ctx)

	if err := u.app.SetRoot(root, true).EnableMouse(true).Run(); err != nil {
		return err
	}
	return nil
}

func (u *UI) promptSearch(root *tview.Flex) {
	input := tview.NewInputField().SetLabel("/").SetFieldWidth(40)
	input.SetDoneFunc(func(key tcell.Key) {
		u.search = input.GetText()
		u.app.SetRoot(root, true)
		u.redraw()
	})
	u.app.SetRoot(input, true)
}

func (u *UI) subscribeAndRedraw(ctx context.Context) {
	ch, cancel := u.bus.Subscribe("tui",
		events.KindNewBlock, events.KindRoundChanged, events.KindVoteReceived,
		events.KindValidatorSetUpdated, events.KindStatusUpdated, events.KindUpgradePlanUpdated,
		events.KindBlockTimeUpdated, events.KindDivergenceDetected, events.KindDivergenceResolved,
		events.KindConnectionLost, events.KindConnectionRestored,
	)
	defer cancel()

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			u.app.Stop()
			return
		case <-ch:
			dirty = true
		case <-tick.C:
			if dirty && !u.paused {
				dirty = false
				u.redraw()
			}
		}
	}
}

func (u *UI) redraw() {
	if u.app == nil {
		return
	}
	u.app.QueueUpdateDraw(func() {
		s := u.st.Snapshot()
		u.renderChain(s)
		u.renderValidators(s)
		u.renderDivergence()
		u.renderStatus(s)
	})
}

func (u *UI) renderChain(s state.StateData) {
	var b strings.Builder
	if s.NodeStatus != nil {
		fmt.Fprintf(&b, "[white::b]%s[-]  v%s\n", s.NodeStatus.Network, s.NodeStatus.CometVersion)
	}
	fmt.Fprintf(&b, "h=[yellow]%d[-]  r=[yellow]%d[-]  step=[yellow]%d[-]\n", s.Height, s.Round, s.Step)
	if !s.StartTime.IsZero() {
		fmt.Fprintf(&b, "elapsed: %s\n", time.Since(s.StartTime).Round(time.Millisecond))
	}
	if s.BlockTime > 0 {
		fmt.Fprintf(&b, "avg block time: %s\n", s.BlockTime.Round(time.Millisecond))
	}
	if s.Upgrade != nil {
		blocksUntil := s.Upgrade.Height - s.Height
		fmt.Fprintf(&b, "[red]upgrade %q at %d (in %d blocks)[-]\n", s.Upgrade.Name, s.Upgrade.Height, blocksUntil)
	}
	if s.LastRound != nil {
		voted, agreed := 0, 0
		for _, v := range s.LastRound.Validators {
			if v.RoundVote.Precommit.Kind != state.VoteAbsent && v.RoundVote.Precommit.Kind != state.VoteNil {
				voted++
			}
			if v.RoundVote.Precommit.Kind == state.VoteForBlock {
				agreed++
			}
		}
		fmt.Fprintf(&b, "precommits: %d/%d (agreed %d)\n", voted, len(s.LastRound.Validators), agreed)
	}
	if s.ConsensusError != nil {
		fmt.Fprintf(&b, "[red]consensus error: %s[-]\n", s.ConsensusError)
	}
	if s.ValidatorsError != nil {
		fmt.Fprintf(&b, "[red]validators error: %s[-]\n", s.ValidatorsError)
	}
	u.chainPane.SetText(b.String())
}

func (u *UI) renderValidators(s state.StateData) {
	t := u.valTable
	t.Clear()
	headers := []string{"#", "moniker", "VP%", "pv", "pc"}
	for i, h := range headers {
		t.SetCell(0, i, tview.NewTableCell("[::b]"+h).SetAlign(tview.AlignLeft).SetSelectable(false))
	}
	if s.LastRound == nil {
		return
	}
	rows := append([]state.ValidatorWithVote(nil), s.LastRound.Validators...)
	switch u.sortMode {
	case "power":
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].Validator.VotingPower.Cmp(rows[j].Validator.VotingPower) > 0
		})
	case "moniker":
		sort.SliceStable(rows, func(i, j int) bool {
			return moniker(rows[i]) < moniker(rows[j])
		})
	}
	if u.search != "" {
		search := strings.ToLower(u.search)
		filtered := rows[:0]
		for _, r := range rows {
			if strings.Contains(strings.ToLower(moniker(r)), search) ||
				strings.Contains(strings.ToLower(r.Validator.Address), search) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	for i, r := range rows {
		row := i + 1
		idxCell := tview.NewTableCell(fmt.Sprintf("%3d", r.Validator.Index+1))
		mon := moniker(r)
		if r.RoundVote.IsProposer {
			if u.disableEmoji {
				mon = "[P] " + mon
			} else {
				mon = "👑 " + mon
			}
		}
		mc := tview.NewTableCell(truncate(mon, 28))
		// Color precedence: proposer (forest green) > our node (turquoise).
		switch {
		case r.RoundVote.IsProposer:
			mc.SetTextColor(tcell.ColorForestGreen)
			idxCell.SetTextColor(tcell.ColorForestGreen)
		case s.NodeStatus != nil && r.Validator.Address == s.NodeStatus.OurValidator:
			mc.SetTextColor(tcell.ColorMediumTurquoise)
		}
		t.SetCell(row, 0, idxCell)
		t.SetCell(row, 1, mc)
		t.SetCell(row, 2, tview.NewTableCell(fmt.Sprintf("%5.2f", r.Validator.VotingPowerPercent)))
		t.SetCell(row, 3, tview.NewTableCell(voteSym(r.RoundVote.Prevote, u.disableEmoji)))
		t.SetCell(row, 4, tview.NewTableCell(voteSym(r.RoundVote.Precommit, u.disableEmoji)))
	}
}

func (u *UI) renderDivergence() {
	if u.tracker == nil {
		return
	}
	rep := u.tracker.CurrentReport()
	var b strings.Builder
	if len(rep.Live) == 0 && len(rep.History) == 0 {
		b.WriteString("(waiting for votes...)\n")
	}
	for _, r := range rep.Live {
		fmt.Fprintf(&b, "[white::b]h%d r%d %s[-]\n", r.Height, r.Round, r.Type)
		for _, g := range r.Groups {
			label := short(g.BlockIDHash)
			if g.BlockIDHash == "" {
				label = "<nil>"
			}
			color := "white"
			if r.IsDivergent && len(r.Groups) > 1 && g.BlockIDHash != r.Groups[0].BlockIDHash && g.BlockIDHash != "" {
				color = "red"
			}
			fmt.Fprintf(&b, "  [%s]%-12s %5.1f%%  (%d v)[-]\n", color, label, g.VotingPowerPct, g.ValidatorCount)
		}
	}
	if len(rep.History) > 0 {
		b.WriteString("\n[gray]recent divergent rounds:[-]\n")
		for i := len(rep.History) - 1; i >= 0 && i >= len(rep.History)-5; i-- {
			r := rep.History[i]
			label := short(r.CanonicalHash)
			if label == "" {
				label = "?"
			}
			fmt.Fprintf(&b, "  h%d r%d %s -> canonical %s\n", r.Height, r.Round, r.Type, label)
		}
	}
	u.divPane.SetText(b.String())
}

func (u *UI) renderStatus(s state.StateData) {
	parts := []string{}
	parts = append(parts, fmt.Sprintf("rpc=%s", short(s.ActiveRPC)))
	parts = append(parts, fmt.Sprintf("sort=%s", u.sortMode))
	if u.search != "" {
		parts = append(parts, fmt.Sprintf("search=%q", u.search))
	}
	if u.paused {
		parts = append(parts, "[yellow]PAUSED[-]")
	}
	parts = append(parts, "  [q]uit  [s]ort  [/] search  [p]ause")
	u.statusBar.SetText(strings.Join(parts, "  "))
}

// helpers

func moniker(v state.ValidatorWithVote) string {
	if v.ChainValidator != nil && v.ChainValidator.Moniker != "" {
		return v.ChainValidator.Moniker
	}
	return v.Validator.Address[:12]
}

func voteSym(v state.Vote, disableEmoji bool) string {
	if disableEmoji {
		switch v.Kind {
		case state.VoteForBlock:
			return "X"
		case state.VoteNil:
			return "·"
		case state.VoteZero:
			return "0"
		}
		return " "
	}
	switch v.Kind {
	case state.VoteForBlock:
		return "✅"
	case state.VoteNil:
		return "❌"
	case state.VoteZero:
		return "🤷"
	}
	return " "
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func nextSort(s string) string {
	switch s {
	case "power":
		return "moniker"
	case "moniker":
		return "power"
	}
	return "power"
}
