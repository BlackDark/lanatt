//go:build !linux

package daemon

import (
	"errors"
	"net"
)

// peerUID exists so the package builds off Linux; the daemon only runs on Linux.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peer credentials need Linux")
}
