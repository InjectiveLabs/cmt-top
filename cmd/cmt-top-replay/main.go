// cmt-top-replay serves only synthetic chain data on a loopback listener.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/testutil/replay"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18657", "loopback mock RPC, WS and LCD address")
	profile := flag.String("profile", "mainnet45", "mainnet45 or testnet5")
	seed := flag.Int64("seed", 1, "deterministic validator seed")
	rounds := flag.Int("rounds", 1, "rounds per height (1..256)")
	stalled := flag.Bool("stalled", false, "retain one stalled height after all rounds")
	blockInterval := flag.Duration("block-interval", time.Second, "pause between heights")
	eventInterval := flag.Duration("event-interval", time.Millisecond, "spacing between source events")
	history := flag.Int("history-blocks", 120, "warm-up committed block history")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("replay must bind a literal loopback address")
	}
	src, err := replay.New(replay.Options{Profile: *profile, Seed: *seed, Rounds: *rounds, Stalled: *stalled, BlockInterval: *blockInterval, EventInterval: *eventInterval, HistoryBlocks: *history})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	srv := &http.Server{Addr: *listen, Handler: src, ReadHeaderTimeout: 5 * time.Second}
	go src.Run(ctx)
	go func() {
		<-ctx.Done()
		src.Close()
		shutdown, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "synthetic %s seed=%d on http://%s (no external network access)\n", *profile, *seed, *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
