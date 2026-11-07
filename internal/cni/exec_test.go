package cni

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T, script string) (Executor, Invocation) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "macvlan"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	ns, err := os.Open("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ns.Close() })
	return Executor{dir, time.Second}, Invocation{"ADD", "workload", "lan0", json.RawMessage(`{"cniVersion":"1.0.0","name":"test","type":"macvlan","ipam":{"type":"static"}}`), ns}
}

func TestInvocation(t *testing.T) {
	t.Setenv("CNI_ARGS", "untrusted")
	e, in := fixture(t, `test "$CNI_COMMAND" = ADD || exit 2
 test "$CNI_CONTAINERID" = workload || exit 3
 test "$CNI_IFNAME" = lan0 || exit 4
 test -z "$CNI_ARGS" || exit 5
 test -e "$CNI_NETNS" || exit 6
 test -e /proc/self/fd/3 || exit 7
 /bin/sh -c 'exec 3<&-; test -e "$CNI_NETNS"' || exit 8
 cat >/dev/null
 printf '{"cniVersion":"1.0.0","ips":[{"address":"10.0.0.2/24"}]}'`)
	out, err := e.Run(context.Background(), in)
	if err != nil || !json.Valid(out) {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestDeleteMissingNamespace(t *testing.T) {
	e, in := fixture(t, `test "$CNI_COMMAND" = DEL && test -z "$CNI_NETNS"`)
	in.Command, in.NetNS = "DEL", nil
	if _, err := e.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestRejectConfig(t *testing.T) {
	e, in := fixture(t, "exit 99")
	for _, config := range []string{`{}`, `{"cniVersion":"1.0.0","name":"test","type":"../macvlan","ipam":{"type":"static"}}`, `{"cniVersion":"1.0.0","name":"test","type":"macvlan","ipam":{"type":"../evil"}}`} {
		in.Config = json.RawMessage(config)
		if _, err := e.Run(context.Background(), in); err == nil {
			t.Fatalf("accepted %s", config)
		}
	}
}

func TestFailedExecution(t *testing.T) {
	for _, script := range []string{"exit 1", "printf invalid", `printf '{"cniVersion":"0.4.0"}'`, `printf '{"cniVersion":"1.0.0"}'`, `printf '{"cniVersion":"1.0.0","ips":[{"address":"invalid"}]}'`, "exec sleep 5", "head -c 1048577 /dev/zero"} {
		t.Run(script, func(t *testing.T) {
			e, in := fixture(t, script)
			e.Timeout = 50 * time.Millisecond
			if _, err := e.Run(context.Background(), in); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}
