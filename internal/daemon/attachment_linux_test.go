package daemon

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lanatt/internal/claims"
)

func TestAdmissionCleanupContinuesAfterClientDeadline(t *testing.T) {
	pid := startIsolatedWorkload(t)
	s := reserveServer(t)
	s.cfg.Socket = filepath.Join(filepath.Dir(s.cfg.State), "daemon.sock")
	s.cfg.AllowRoot = true
	req := attachReq("retry", "10.254.1.2", "02:00:00:00:00:02")
	req.Pid = pid
	req.Joins = append(req.Joins, attachReqOn("b", "retry", "10.254.2.2", "02:00:00:00:00:03").Joins[0])
	for i, join := range req.Joins {
		netns := uint64(i + 1)
		previous := attachReqOn(join.Network, "previous", join.Address, join.MAC)
		plans, err := s.reserve(1000, previous, target{netns: netns, start: "1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.intend(plans[0]); err != nil {
			t.Fatal(err)
		}
		s.finish(netns)
	}

	// Both records conflict. Hold the first removal until the client deadline expires.
	first := make(chan struct{})
	resume := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(resume) })
	defer unblock()
	calls := 0
	s.remove = func(claims.Pending) (bool, error) {
		calls++
		if calls == 1 {
			close(first)
			<-resume
			return true, nil
		}
		return false, errors.New("second device still pending")
	}

	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			s.serveConn(conn)
		}
		done <- err
	}()
	client, err := net.Dial("unix", s.cfg.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := json.MarshalWrite(client, req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case err := <-done:
		t.Fatalf("request finished before cleanup began: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach cleanup")
	}

	// Expire the client deadline deterministically while the daemon is still in admission.
	if err := client.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("client read: %v, want deadline exceeded", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not finish after cleanup resumed")
	}
	if calls != 2 || len(s.state.Pending) != 1 || len(s.busy) != 0 {
		t.Fatalf("want both cleanup attempts before reservation, got %d attempts, %d pending and %d busy", calls, len(s.state.Pending), len(s.busy))
	}
}

func startIsolatedWorkload(t *testing.T) int {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to unshare a netns")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "unshare", "--net", "sh", "-c", "echo ready; exec sleep 30")
	cmd.Stderr = os.Stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	ready := bufio.NewScanner(output)
	if !ready.Scan() || ready.Text() != "ready" {
		t.Fatal("isolated workload did not start")
	}
	return cmd.Process.Pid
}
