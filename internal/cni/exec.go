// Package cni is an experimental plugin execution seam. It is not wired into
// the daemon: durable ADD/DEL lifecycle and claims integration are prerequisites.
package cni

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const maxOutput = 1 << 20

// Executor runs only administrator-selected plugins from one trusted directory.
// The directory and its contents must not be writable by workload users.
type Executor struct {
	Directory string
	Timeout   time.Duration
}

// Invocation identifies one attachment. Config is administrator-owned CNI JSON,
// not OCI annotation data. NetNS must remain open until Run returns.
type Invocation struct {
	Command     string
	ContainerID string
	Interface   string
	Config      json.RawMessage
	NetNS       *os.File
}

// Run executes one standalone CNI config. Lists/chaining are deliberately absent.
// Result validation checks version and address syntax only; production must also
// enforce interface linkage, network policy, route policy and claim conflicts.
// On ADD failure, the caller must still attempt DEL using the original config.
// Timeout terminates the direct plugin only; production needs descendant cleanup.
func (e Executor) Run(ctx context.Context, in Invocation) (json.RawMessage, error) {
	plugin, err := e.validate(in)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(e.Directory, plugin))
	cmd.WaitDelay = time.Second
	ns := ""
	if in.NetNS != nil {
		// The parent-held path also works in nested IPAM processes, which do not
		// necessarily inherit ExtraFiles. Never derive this path from a workload PID.
		ns = "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.FormatUint(uint64(in.NetNS.Fd()), 10)
		cmd.ExtraFiles = []*os.File{in.NetNS}
	}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "CNI_COMMAND=" + in.Command,
		"CNI_CONTAINERID=" + in.ContainerID, "CNI_IFNAME=" + in.Interface,
		"CNI_NETNS=" + ns, "CNI_PATH=" + e.Directory, "CNI_ARGS="}
	cmd.Stdin = bytes.NewReader(in.Config)
	out, stderr := &boundedBuffer{}, &boundedBuffer{}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("CNI %s %s: %w (stderr: %s)", plugin, in.Command, err, stderr.String())
	}
	if in.Command == "DEL" {
		return nil, nil
	}
	var result struct {
		Version string `json:"cniVersion"`
		IPs     []struct {
			Address string `json:"address"`
		} `json:"ips"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Version != "1.0.0" {
		return nil, errors.New("CNI ADD returned an invalid or unsupported result")
	}
	if len(result.IPs) == 0 {
		return nil, errors.New("CNI ADD returned no addresses")
	}
	for _, ip := range result.IPs {
		if _, err := netip.ParsePrefix(ip.Address); err != nil {
			return nil, fmt.Errorf("invalid CNI result address: %w", err)
		}
	}
	return append(json.RawMessage(nil), out.Bytes()...), nil
}

func (e Executor) validate(in Invocation) (string, error) {
	if !filepath.IsAbs(e.Directory) || e.Timeout <= 0 {
		return "", errors.New("CNI requires an absolute plugin directory and positive timeout")
	}
	if in.Command != "ADD" && in.Command != "DEL" {
		return "", errors.New("unsupported CNI command")
	}
	if in.ContainerID == "" || in.Interface == "" || (in.Command == "ADD" && in.NetNS == nil) {
		return "", errors.New("missing CNI attachment identity or namespace")
	}
	var cfg struct {
		Version string `json:"cniVersion"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		IPAM    struct {
			Type string `json:"type"`
		} `json:"ipam"`
	}
	if err := json.Unmarshal(in.Config, &cfg); err != nil {
		return "", err
	}
	if cfg.Version != "1.0.0" || cfg.Name == "" {
		return "", errors.New("CNI config requires version 1.0.0 and network name")
	}
	switch cfg.Type {
	case "macvlan", "ipvlan", "bridge":
	default:
		return "", errors.New("CNI plugin type is not allowed")
	}
	switch cfg.IPAM.Type {
	case "static", "host-local", "dhcp":
	default:
		return "", errors.New("CNI IPAM type is not allowed")
	}
	return cfg.Type, nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > maxOutput-b.Len() {
		return 0, errors.New("CNI output exceeds limit")
	}
	return b.Buffer.Write(p)
}
