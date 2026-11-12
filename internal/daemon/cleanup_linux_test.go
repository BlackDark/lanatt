package daemon

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"

	"lanatt/internal/claims"
	"lanatt/internal/config"
)

func TestNetnsHeldByThreadIsFound(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to unshare a netns")
	}
	ready := make(chan uint64)
	done := make(chan struct{})
	go func() {
		// setns is per thread: only this OS thread holds the new netns, not the group leader.
		// The thread keeps the test netns, so it stays locked: Go ends a locked thread whose
		// goroutine exits, rather than reusing it with the netns changed.
		runtime.LockOSThread()
		if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
			runtime.UnlockOSThread()
			ready <- 0
			return
		}
		link, err := os.Readlink("/proc/thread-self/ns/net")
		var ino uint64
		if err == nil {
			_, err = fmt.Sscanf(link, "net:[%d]", &ino)
		}
		if err != nil {
			ready <- 0
			return
		}
		ready <- ino
		<-done
	}()
	ino := <-ready
	defer close(done)
	if ino == 0 {
		t.Skip("could not unshare a netns on this host")
	}
	f, err := openNetns(ino)
	if err != nil {
		t.Fatalf("netns held only by a thread not found: %v", err)
	}
	defer f.Close()
	if _, err := inNetns(f, "link", "show"); err != nil {
		t.Fatalf("cannot enter the recovered namespace: %v", err)
	}
}

func TestRemoveOwnedAfterFailedMove(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root and a dummy parent")
	}
	token, err := freeToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := "lp" + token[:12]
	if _, err := run(nil, ipBin, "link", "add", parent, "type", "dummy"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := run(nil, ipBin, "link", "del", "dev", parent); err != nil {
			t.Error(err)
		}
	})
	invalid, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer invalid.Close()
	rec := claims.Pending{Token: token, Temp: "lt" + token[:12]}
	p := claims.Planned{Claim: claims.Claim{MAC: "02:42:00:00:00:01"}}
	n := config.Network{Parent: parent, Driver: config.DriverMacvlan}
	if _, err := join(invalid, n, p, rec.Temp, rec.Token); err == nil {
		t.Fatal("move into an invalid namespace succeeded")
	}
	if _, found, err := findOwned(nil, rec, rec.Temp); err != nil || !found {
		t.Fatalf("failed move did not leave an identifiable host link: %v", err)
	}
	if gone, err := removeOwned(rec, nil); err != nil || !gone {
		t.Fatalf("host link cleanup failed: %v", err)
	}
	if _, found, err := findOwned(nil, rec, rec.Temp); err != nil || found {
		t.Fatalf("host link still present after cleanup: %v", err)
	}
}

func TestRemoveOwnedRetainsUnreachableNamespace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root and Linux iproute2")
	}
	token, err := freeToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := claims.Pending{Claim: claims.Claim{Netns: ^uint64(0)}, Token: token}
	if gone, err := removeOwned(rec, nil); gone || !errors.Is(err, errNoReference) {
		t.Fatalf("unreachable namespace: gone=%v, error=%v", gone, err)
	}
}
