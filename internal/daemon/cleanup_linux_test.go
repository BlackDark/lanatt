package daemon

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
)

func TestNetnsHeldByThreadIsFound(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to unshare a netns")
	}
	ready := make(chan uint64)
	done := make(chan struct{})
	go func() {
		// setns is per thread: only this OS thread holds the new netns, not the group leader.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
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
	f.Close()
}
