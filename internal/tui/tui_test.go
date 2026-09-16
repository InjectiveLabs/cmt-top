package tui

import (
	"math/big"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func startTestUI(t *testing.T) *UI {
	t.Helper()
	u := New(Options{State: state.New(), Bus: events.NewBus(16), Timezone: "UTC"})
	u.st.Mutate(func(s *state.StateData) {
		s.Height = 10
		s.LastRound = &state.RoundView{TotalVP: big.NewInt(100), Validators: []state.ValidatorWithVote{
			{Validator: state.Validator{Address: "AA", VotingPower: big.NewInt(70), VotingPowerPercent: 70}, ChainValidator: &state.ChainValidator{Moniker: "Zulu"}},
			{Validator: state.Validator{Address: "BB", VotingPower: big.NewInt(30), VotingPowerPercent: 30}, ChainValidator: &state.ChainValidator{Moniker: "Alpha"}},
		}}
	})
	screen := tcell.NewSimulationScreen("UTF-8")
	u.app = tview.NewApplication().SetScreen(screen)
	screen.SetSize(110, 32)
	u.initialize()
	drawn := make(chan struct{}, 1)
	u.app.SetAfterDrawFunc(func(tcell.Screen) {
		select {
		case drawn <- struct{}{}:
		default:
		}
	})
	done := make(chan error, 1)
	go func() { done <- u.app.Run() }()
	t.Cleanup(func() {
		u.app.Stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("UI run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("UI event loop failed to stop")
		}
	})
	select {
	case <-drawn:
	case err := <-done:
		t.Fatalf("UI exited before first draw: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("UI did not draw")
	}
	return u
}

func onUI(t *testing.T, u *UI, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { u.app.QueueUpdate(fn); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("UI callback deadlocked its event loop")
	}
}

// Dispatch through the actual global capture and focused primitive while on the
// application event loop. Calling a blocking redraw from either callback would
// prevent onUI from completing, reproducing the original sort/search deadlock.
func pressUI(t *testing.T, u *UI, key tcell.Key, r rune) {
	t.Helper()
	onUI(t, u, func() {
		ev := tcell.NewEventKey(key, r, tcell.ModNone)
		if capture := u.app.GetInputCapture(); capture != nil {
			ev = capture(ev)
		}
		if ev != nil {
			if focus := u.app.GetFocus(); focus != nil {
				if handler := focus.InputHandler(); handler != nil {
					handler(ev, func(p tview.Primitive) { u.app.SetFocus(p) })
				}
			}
		}
	})
}

func TestSortAndSearchCallbacksRemainResponsive(t *testing.T) {
	u := startTestUI(t)
	var initial string
	onUI(t, u, func() { initial = u.chainPane.GetText(true) })
	if !strings.Contains(initial, "unavailable") {
		t.Fatalf("initial upstream health is missing: %q", initial)
	}
	pressUI(t, u, tcell.KeyRune, 's')
	var sortMode, moniker string
	onUI(t, u, func() { sortMode = u.sortMode; moniker = u.valTable.GetCell(1, 1).Text })
	if sortMode != "moniker" || moniker != "Alpha" {
		t.Fatalf("sort did not update: %q first=%q", sortMode, moniker)
	}
	pressUI(t, u, tcell.KeyRune, '/')
	for _, r := range "qps" {
		pressUI(t, u, tcell.KeyRune, r)
	}
	pressUI(t, u, tcell.KeyEnter, 0)
	var search string
	var searching, paused bool
	onUI(t, u, func() { search = u.search; searching = u.searching; paused = u.paused; sortMode = u.sortMode })
	if search != "qps" || searching || paused || sortMode != "moniker" {
		t.Fatalf("search characters triggered global shortcuts: search=%q searching=%t paused=%t sort=%s", search, searching, paused, sortMode)
	}
	pressUI(t, u, tcell.KeyRune, '/')
	pressUI(t, u, tcell.KeyRune, 'x')
	pressUI(t, u, tcell.KeyEscape, 0)
	onUI(t, u, func() { search = u.search; searching = u.searching })
	if search != "qps" || searching {
		t.Fatalf("Escape did not cancel search: search=%q searching=%t", search, searching)
	}
}

func TestPauseFreezesDisplayedStateAndResumesFreshSnapshot(t *testing.T) {
	u := startTestUI(t)
	pressUI(t, u, tcell.KeyRune, 'p')
	var status string
	onUI(t, u, func() { status = u.statusBar.GetText(true) })
	if !strings.Contains(status, "PAUSED") {
		t.Fatalf("pause was not displayed immediately: %q", status)
	}
	u.st.Mutate(func(s *state.StateData) { s.Height = 20 })
	pressUI(t, u, tcell.KeyRune, 's')
	var displayedHeight int64
	onUI(t, u, func() { displayedHeight = u.displayed.Height })
	if displayedHeight != 10 {
		t.Fatalf("sorting while paused advanced visible state to %d", displayedHeight)
	}
	pressUI(t, u, tcell.KeyRune, 'p')
	onUI(t, u, func() { displayedHeight = u.displayed.Height; status = u.statusBar.GetText(true) })
	if displayedHeight != 20 || strings.Contains(status, "PAUSED") {
		t.Fatalf("resume did not reconcile latest snapshot: height=%d status=%q", displayedHeight, status)
	}
}

func TestUnicodeTruncationPreservesCharactersAndTerminalWidth(t *testing.T) {
	for _, text := range []string{"東京のバリデータ", "👩‍💻 validator team", "ééééé", "ordinary validator"} {
		for _, width := range []int{0, 1, 3, 7} {
			got := truncate(text, width)
			if !utf8.ValidString(got) || uniseg.StringWidth(got) > width {
				t.Fatalf("truncate(%q,%d) = %q (width %d)", text, width, got, uniseg.StringWidth(got))
			}
		}
	}
}
