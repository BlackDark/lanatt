package wire

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestOmitsZeroPidAndDefault(t *testing.T) {
	b, err := json.Marshal(Request{Op: OpAttach, ID: "c", Joins: []Join{{Network: "n", Address: "10.0.0.2", MAC: "02:00:00:00:00:02"}}})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, `"pid"`) || strings.Contains(s, `"default"`) {
		t.Fatalf("zero values encoded: %s", s)
	}
}

// Call must finish without the peer closing its write side: the server reads one value and answers.
func TestCallRoundTripWithoutHalfClose(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req Request
		if err := json.UnmarshalDecode(jsontext.NewDecoder(conn), &req, json.RejectUnknownMembers(true)); err != nil {
			return
		}
		_ = json.MarshalWrite(conn, Response{OK: req.ID == "c", Error: "nope"})
	}()
	done := make(chan error, 1)
	go func() { done <- Call(sock, Request{Op: OpAttach, ID: "c"}, 5*time.Second) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Call hung")
	}
}
