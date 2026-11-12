// Package hook is the OCI hook side: it runs as the caller and only forwards the join.
package hook

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lanatt/internal/wire"
)

// State is the part of the OCI runtime state the hook reads on stdin.
type State struct {
	ID     string `json:"id"`
	Pid    int    `json:"pid"`
	Bundle string `json:"bundle"`
}

// Request turns the OCI state and the bundle's config.json into a daemon request.
// A nil request means the container has no annotation and nothing is to be done.
func Request(stage string, stdin io.Reader, readFile func(string) ([]byte, error)) (*wire.Request, error) {
	if stage != "create" && stage != "stop" {
		return nil, fmt.Errorf("unknown hook stage %q, want create or stop", stage)
	}
	var st State
	if err := json.UnmarshalDecode(jsontext.NewDecoder(stdin), &st); err != nil {
		return nil, fmt.Errorf("OCI state: %w", err)
	}
	data, err := readFile(filepath.Join(st.Bundle, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("bundle config: %w", err)
	}
	var spec struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("bundle config: %w", err)
	}
	value, ok := spec.Annotations[wire.AnnotationKey]
	if !ok {
		return nil, nil
	}
	if stage == "stop" {
		return &wire.Request{Op: wire.OpDetach, ID: st.ID}, nil
	}
	// Strict: a misspelt field (say "ipv6") would otherwise join without it, silently. Trailing
	// data would be ignored the same way, so the value must hold exactly one array.
	var joins []wire.Join
	dec := jsontext.NewDecoder(strings.NewReader(value))
	if err := json.UnmarshalDecode(dec, &joins, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("annotation %s: %w", wire.AnnotationKey, err)
	}
	var extra jsontext.Value
	if err := json.UnmarshalDecode(dec, &extra); err != io.EOF {
		return nil, fmt.Errorf("annotation %s: data after the join array", wire.AnnotationKey)
	}
	return &wire.Request{Op: wire.OpAttach, ID: st.ID, Pid: st.Pid, Joins: joins}, nil
}

// ErrUsage is returned for a bad command line.
var ErrUsage = errors.New("usage: lanatt hook create|stop [-socket path]")

// Run is the hook command.
func Run(stage string, stdin io.Reader, call func(wire.Request) error) error {
	req, err := Request(stage, stdin, os.ReadFile)
	if err != nil || req == nil {
		return err
	}
	return call(*req)
}
