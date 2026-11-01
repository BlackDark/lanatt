// Package wire is the socket contract between the hook and the daemon.
package wire

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// AnnotationKey is the OCI annotation that carries the joins.
const AnnotationKey = "io.lanatt.attachments"

// DefaultSocket is where the daemon listens unless config says otherwise.
const DefaultSocket = "/run/lanatt/lanatt.sock"

const (
	OpAttach = "attach"
	OpDetach = "detach"
)

// Join is one requested attachment, as written in the annotation.
type Join struct {
	Network   string `json:"network"`
	Address   string `json:"address"`
	MAC       string `json:"mac"`
	Interface string `json:"interface,omitempty"`
	Default   bool   `json:"default,omitempty"`
}

// Request is the single JSON object a client sends.
type Request struct {
	Op    string `json:"op"`
	ID    string `json:"id"`
	Pid   int    `json:"pid,omitempty"`
	Joins []Join `json:"joins,omitempty"`
}

// Response is the single JSON object the daemon answers with.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Call sends req to the daemon at socket and returns its refusal as an error.
func Call(socket string, req Request, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return fmt.Errorf("connect to lanatt daemon: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("lanatt refused: %s", resp.Error)
	}
	return nil
}
