package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"

	"lanatt/internal/claims"
	"lanatt/internal/wire"
)

// aliasPrefix marks a device lanatt created. The alias carries the attach's token.
const aliasPrefix = "lanatt:"

// errUnreachable means no process is in the record's netns, so its devices cannot be checked.
var errUnreachable = errors.New("netns has no process to enter")

// newToken is the random identity of one attach's devices: it is the host-side temp name's
// middle and the alias. Eight hex digits keep the name within IFNAMSIZ.
func newToken() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// TempName is the host-side name of device i of an attach with token. It matches StaleTemps.
func TempName(token string, i int) string {
	return fmt.Sprintf("lt%s%d", token, i)
}

// ownedName finds the device in `ip -j link show` output that carries token, or is temp with
// no other alias. Other devices, including one of the same name with another token, are not ours.
func ownedName(out, token, temp string) (string, bool, error) {
	var links []struct {
		Ifname  string `json:"ifname"`
		Ifalias string `json:"ifalias"`
	}
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		return "", false, fmt.Errorf("parse links: %w", err)
	}
	for _, l := range links {
		if l.Ifalias == aliasPrefix+token {
			return l.Ifname, true, nil
		}
		if temp != "" && l.Ifname == temp && (l.Ifalias == "" || l.Ifalias == aliasPrefix+token) {
			return l.Ifname, true, nil
		}
	}
	return "", false, nil
}

// findOwned looks for rec's device in ns; a nil ns is the host. Only the host matches by temp name,
// since the name moves with the device.
func findOwned(ns *os.File, rec claims.Pending, temp string) (string, bool, error) {
	var out string
	var err error
	if ns == nil {
		out, err = run(nil, ipBin, "-j", "link", "show")
	} else {
		out, err = inNetns(ns, "-j", "link", "show")
	}
	if err != nil {
		return "", false, err
	}
	return ownedName(out, rec.Token, temp)
}

// removeOwned deletes rec's device on the host and in its netns, using ns if given, else the
// netns found through any process in it. It reports gone only when both checks find nothing.
func removeOwned(rec claims.Pending, ns *os.File) (bool, error) {
	if name, ok, err := findOwned(nil, rec, rec.Temp); err != nil {
		return false, err
	} else if ok {
		if _, err := run(nil, ipBin, "link", "del", "dev", name); err != nil {
			return false, err
		}
	}
	if ns == nil {
		var err error
		if ns, err = openNetns(rec.Netns); err != nil {
			return false, err
		}
		defer ns.Close()
	} else if key, kerr := fileNsID(ns); kerr != nil || key.ino != rec.Netns {
		return false, fmt.Errorf("netns %d: %w", rec.Netns, errors.Join(errUnreachable, kerr))
	}
	name, ok, err := findOwned(ns, rec, "")
	if err != nil {
		return false, err
	}
	if ok {
		if _, err := inNetns(ns, "link", "del", "dev", name); err != nil {
			return false, err
		}
	}
	return true, nil
}

// openNetns opens the netns with inode ino through any process in it. The fd is checked after
// open: a pid can exit and its number be reused.
func openNetns(ino uint64) (*os.File, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		path := fmt.Sprintf("/proc/%d/ns/net", pid)
		key, err := nsID(path)
		if err != nil || key.ino != ino {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if got, err := fileNsID(f); err == nil && got.ino == ino {
			return f, nil
		}
		f.Close()
	}
	return nil, fmt.Errorf("netns %d: %w", ino, errUnreachable)
}

// conflicts reports whether rec holds something req would take: its netns, or its address or
// MAC on the same network or parent. A field that does not parse conflicts with nothing; Plan
// refuses it later.
func (s *Server) conflicts(rec claims.Pending, req wire.Request, netns uint64) bool {
	if rec.Netns == netns {
		return true
	}
	for _, j := range req.Joins {
		n, ok := s.cfg.Networks[j.Network]
		if !ok {
			continue
		}
		if rec.Network == j.Network && (sameAddr(rec.Address, j.Address) || sameAddr(rec.Address6, j.Address6)) {
			return true
		}
		if rec.MAC != "" && rec.Parent == n.Parent && sameMAC(rec.MAC, j.MAC) {
			return true
		}
	}
	return false
}

func sameAddr(held netip.Addr, s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && held.IsValid() && a == held
}

func sameMAC(held, s string) bool {
	hw, err := net.ParseMAC(s)
	return err == nil && hw.String() == held
}
