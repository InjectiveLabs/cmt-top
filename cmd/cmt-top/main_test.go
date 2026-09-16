package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each CLI invocation runs in its own process, matching production ownership of
// signal handlers and the Prometheus default registry.
func TestCLIProcessHelper(t *testing.T) {
	if os.Getenv("CMTOP_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"cmt-top"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func fixtureConfig(t *testing.T, metricsAddr string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := fmt.Sprintf("[chain]\nlcd = %q\n[[chain.rpc]]\nurl = %q\nprimary = true\n[obs]\nmetrics_listen = %q\n", "http://127.0.0.1:1", "http://127.0.0.1:1", metricsAddr)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func cliProcess(ctx context.Context, args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestCLIProcessHelper$", "--"}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CMTOP_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "CMTOP_TEST_HELPER=1")
	output := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = output, output
	return cmd, output
}

func TestOccupiedWebPortTerminatesWithFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd, output := cliProcess(ctx, "--config", fixtureConfig(t, ""), "--mode", "web", "--web-listen", listener.Addr().String())
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI stayed alive after its required web listener failed: %s", output.String())
	}
	if err == nil {
		t.Fatal("occupied web port exited successfully")
	}
	if !strings.Contains(output.String(), "web:") || !strings.Contains(output.String(), "address already in use") {
		t.Fatalf("CLI failed for an unrelated reason: %v\n%s", err, output.String())
	}
}

func TestHeadlessServesMetricsWithoutStartingWeb(t *testing.T) {
	// An occupied web address proves headless mode does not even attempt to
	// start a web listener: such an attempt must fail and stop the process.
	webListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer webListener.Close()
	metricsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsAddr := metricsListener.Addr().String()
	metricsListener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd, output := cliProcess(ctx, "--config", fixtureConfig(t, metricsAddr), "--mode", "headless", "--web-listen", webListener.Addr().String())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	ready := false
	for !ready {
		select {
		case err := <-done:
			t.Fatalf("headless exited before serving metrics: %v\n%s", err, output.String())
		case <-ctx.Done():
			<-done
			t.Fatalf("headless never served metrics: %s", output.String())
		case <-tick.C:
			response, err := client.Get("http://" + metricsAddr + "/metrics")
			if err != nil {
				continue
			}
			ready = response.StatusCode == http.StatusOK
			response.Body.Close()
		}
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("headless did not shut down gracefully: %v\n%s", err, output.String())
		}
	case <-ctx.Done():
		<-done
		t.Fatalf("headless did not stop on interrupt: %s", output.String())
	}
	if strings.Contains(output.String(), "web listening") {
		t.Fatalf("headless started web: %s", output.String())
	}
}
