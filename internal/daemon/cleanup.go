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
	"slices"
	"strconv"
	"strings"
	"time"

	"lanatt/internal/claims"
	"lanatt/internal/wire"
)

// aliasPrefix marks a device lanatt created. The alias carries the attach's token.
const aliasPrefix = "lanatt:"

// freeToken draws a per-device alias token. Completed devices also keep their aliases,
// so the full token is used for ownership even though the temporary name is shorter.
func freeToken(pending []claims.Pending) (string, error) {
	for range 16 {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		token := hex.EncodeToString(b[:])
		if !slices.ContainsFunc(pending, func(r claims.Pending) bool { return r.Token == token }) {
			return token, nil
		}
	}
	return "", errors.New("no free cleanup token")
}

// ownedName requires an alias match. An unmarked temporary link could be a device
// left by a crash before tagging or a preexisting link that caused creation to fail.
func ownedName(out, token, temp string) (string, bool, error) {
	var links []struct {
		Ifname  string `json:"ifname"`
		Ifalias string `json:"ifalias"`
	}
	if err := json.Unmarshal([]byte(out), &links); err != nil {
		return "", false, fmt.Errorf("parse links: %w", err)
	}
	var name, unmarked string
	for _, l := range links {
		if l.Ifalias == aliasPrefix+token {
			if name != "" {
				return "", false, errors.New("multiple links carry the cleanup token")
			}
			name = l.Ifname
		}
		if l.Ifname == temp && l.Ifalias == "" {
			unmarked = l.Ifname
		}
	}
	if name == "" && unmarked != "" {
		return "", false, fmt.Errorf("ownership of temporary link %s is unverified", unmarked)
	}
	return name, name != "", nil
}

// findOwned looks for rec's device in ns. A nil ns is the host, where the device might
// have been created but never tagged.
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

var errNoReference = errors.New("network namespace could not be reached through a visible reference")

// removeOwned checks the host first because creation and movement are separate operations.
// When the token is absent on the host, only inspection of the target can verify absence.
func removeOwned(rec claims.Pending, ns *os.File) (bool, error) {
	if name, ok, err := findOwned(nil, rec, rec.Temp); err != nil {
		return false, err
	} else if ok {
		_, err := run(nil, ipBin, "link", "del", "dev", name)
		return err == nil, err
	}
	if ns == nil {
		var err error
		ns, err = openNetns(rec.Netns)
		if err != nil {
			return false, fmt.Errorf("netns %d: %w", rec.Netns, err)
		}
		defer ns.Close()
	} else if key, err := fileNsID(ns); err != nil || key.ino != rec.Netns {
		return false, fmt.Errorf("netns %d: attach's fd is another netns", rec.Netns)
	}
	name, ok, err := findOwned(ns, rec, "")
	if err != nil || !ok {
		return err == nil, err
	}
	_, err = inNetns(ns, "link", "del", "dev", name)
	return err == nil, err
}

// openNetns searches for a usable reference. Missing tasks and inaccessible entries can
// be skipped because a search miss always retains the record.
func openNetns(ino uint64) (*os.File, error) {
	until := time.Now().Add(childTimeout)
	key, err := nsID("/proc/self/ns/net")
	if err != nil {
		return nil, err
	}
	key.ino = ino
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	link := fmt.Sprintf("net:[%d]", ino)
	for _, proc := range procs {
		if time.Now().After(until) {
			return nil, errNoReference
		}
		if _, err := strconv.Atoi(proc.Name()); err != nil {
			continue
		}
		dir := "/proc/" + proc.Name() + "/task"
		tasks, _ := os.ReadDir(dir)
		for _, task := range tasks {
			if time.Now().After(until) {
				return nil, errNoReference
			}
			t := dir + "/" + task.Name()
			if f := openIfNetns(t+"/ns/net", key); f != nil {
				return f, nil
			}
			if f := searchTaskReferences(t, link, key, until); f != nil {
				return f, nil
			}
		}
	}
	return nil, errNoReference
}

func searchTaskReferences(task, link string, key nsKey, until time.Time) *os.File {
	fds, _ := os.ReadDir(task + "/fd")
	for _, fd := range fds {
		if time.Now().After(until) {
			return nil
		}
		path := task + "/fd/" + fd.Name()
		if target, _ := os.Readlink(path); target == link {
			if f := openIfNetns(path, key); f != nil {
				return f
			}
		}
	}
	// mountinfo lists only mounts under this task's root, so a task with a narrower
	// root cannot stand in for another task in the same mount namespace.
	data, _ := os.ReadFile(task + "/mountinfo")
	for _, mount := range parseNsfsMounts(string(data)) {
		if time.Now().After(until) {
			return nil
		}
		if f := openIfNetns(task+"/root"+mount, key); f != nil {
			return f
		}
	}
	return nil
}

func openIfNetns(path string, key nsKey) *os.File {
	// Linux O_PATH pins the object without opening it for I/O. A task can replace
	// a mount path during lookup, so inspect the pinned object before using it.
	const linuxOpenPath = 0x200000
	f, err := os.OpenFile(path, linuxOpenPath, 0)
	if err != nil {
		return nil
	}
	if candidate, err := fileNsID(f); err == nil && candidate == key {
		return f
	}
	f.Close()
	return nil
}

// parseNsfsMounts picks the mount points of nsfs entries in mountinfo output.
func parseNsfsMounts(out string) []string {
	var mounts []string
	for _, line := range strings.Split(out, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fl, rf := strings.Fields(left), strings.Fields(right)
		if len(fl) < 5 || len(rf) == 0 || rf[0] != "nsfs" {
			continue
		}
		mounts = append(mounts, mountInfoUnescaper.Replace(fl[4]))
	}
	return mounts
}

// mountInfoUnescaper undoes the octal escapes the kernel uses in mountinfo paths.
var mountInfoUnescaper = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

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
