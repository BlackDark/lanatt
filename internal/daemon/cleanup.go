package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"lanatt/internal/claims"
	"lanatt/internal/wire"
)

// aliasPrefix marks a device lanatt created. The alias carries the attach's token.
const aliasPrefix = "lanatt:"

// newToken is the random identity of one attach's devices: it is the host-side temp name's
// middle and the alias. Eight hex digits keep the name within IFNAMSIZ.
func newToken() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// freeToken draws a token no record in pending uses. The alias must be unique among our devices,
// or retiring one record would match another's device.
func freeToken(pending []claims.Pending) (string, error) {
	for range 16 {
		token, err := newToken()
		if err != nil {
			return "", err
		}
		if !slices.ContainsFunc(pending, func(r claims.Pending) bool { return r.Token == token }) {
			return token, nil
		}
	}
	return "", errors.New("no free cleanup token")
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

// errNoReference means nothing holds the netns: it is destroyed, and its devices went with it.
var errRaced = errors.New("netns lookup raced with a task exit; retry")

var errNoReference = errors.New("no process, fd or mount holds the netns")

// removeOwned deletes rec's device on the host and in its netns, using ns if given. A netns
// with no reference left is destroyed, so its device is gone once the host check misses it.
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
		if ns, err = openNetns(rec.Netns); errors.Is(err, errNoReference) {
			return true, nil
		} else if err != nil {
			return false, err
		}
		defer ns.Close()
	} else if key, err := fileNsID(ns); err != nil || key.ino != rec.Netns {
		return false, fmt.Errorf("netns %d: attach's fd is another netns", rec.Netns)
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

// openNetns opens the netns with inode ino through any reference: a task in it, an fd to it, or
// an nsfs mount of it in any mount namespace. setns is per thread, so every task is checked.
// A task exiting mid-scan may have hidden a reference, so the scan retries; if it keeps racing,
// the result is errRaced, not absence.
func openNetns(ino uint64) (*os.File, error) {
	for range scanAttempts {
		f, err := scanNetns(ino)
		if !errors.Is(err, errRaced) {
			return f, err
		}
	}
	return nil, errRaced
}

// scanAttempts bounds how often a racing scan of openNetns is repeated.
const scanAttempts = 3

// scanNetns is one pass of openNetns. Each fd is checked after open, since a task can exit and its
// id be reused. Any task that exits during the pass returns errRaced.
func scanNetns(ino uint64) (*os.File, error) {
	tasks, err := procTasks()
	if err != nil {
		return nil, err
	}
	raced := false
	for _, t := range tasks {
		f, ok, err := openIfNetns(t+"/ns/net", ino)
		if exited(t, err) {
			raced = true
			continue
		} else if err != nil {
			return nil, err
		}
		if ok {
			return f, nil
		}
	}
	for _, t := range tasks {
		f, fdRaced, err := scanFds(t, ino)
		if err != nil {
			return nil, err
		}
		if f != nil {
			return f, nil
		}
		raced = raced || fdRaced
	}
	// Every task is scanned: mountinfo lists only the mounts under that task's root, so a
	// namespace's narrower-rooted task cannot stand in for a wider one.
	for _, t := range tasks {
		data, err := os.ReadFile(t + "/mountinfo")
		// A zombie has no namespaces left: its mountinfo fails with EINVAL, and it holds nothing.
		if errors.Is(err, syscall.EINVAL) {
			continue
		} else if exited(t, err) {
			raced = true
			continue
		} else if err != nil {
			return nil, err
		}
		for _, m := range parseNsfsMounts(string(data)) {
			f, ok, err := openIfNetns(t+"/root"+m, ino)
			if exited(t, err) {
				raced = true
				break
			} else if err != nil {
				return nil, err
			}
			if ok {
				return f, nil
			}
		}
	}
	if raced {
		return nil, errRaced
	}
	return nil, errNoReference
}

// scanFds looks for an fd of task t that is the netns ino. The fd directory stays open while its
// links are read, so the scan's own handle never shows as closed; an fd that closes mid-scan is a
// race, since it may have been duplicated into a task scanned earlier.
func scanFds(t string, ino uint64) (f *os.File, raced bool, err error) {
	dir, err := os.Open(t + "/fd")
	if exited(t, err) {
		return nil, true, nil
	} else if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if exited(t, err) {
		return nil, true, nil
	} else if err != nil {
		return nil, false, err
	}
	for _, name := range names {
		path := t + "/fd/" + name
		target, err := os.Readlink(path)
		if exited(t, err) {
			raced = true
			continue
		} else if err != nil {
			return nil, false, err
		}
		if target != fmt.Sprintf("net:[%d]", ino) {
			continue
		}
		f, ok, err := openIfNetns(path, ino)
		if exited(t, err) || (err == nil && !ok) {
			raced = true
			continue
		} else if err != nil {
			return nil, false, err
		}
		if ok {
			return f, false, nil
		}
	}
	return nil, raced, nil
}

// taskGone reports whether the task directory dir no longer exists.
func taskGone(dir string) bool {
	_, err := os.Stat(dir)
	return errors.Is(err, fs.ErrNotExist)
}

// exited reports whether err came from a task that has exited, given its /proc directory dir.
// Linux returns EACCES, not ENOENT, for ns links of a dead task, so dir is checked too.
// An error from a live task is real.
func exited(dir string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	_, statErr := os.Stat(dir)
	return errors.Is(statErr, fs.ErrNotExist)
}

// openIfNetns opens path and reports whether it is the netns ino.
func openIfNetns(path string, ino uint64) (*os.File, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	key, err := fileNsID(f)
	if err != nil {
		f.Close()
		return nil, false, err
	}
	if key.ino == ino {
		return f, true, nil
	}
	f.Close()
	return nil, false, nil
}

// procPids lists the numeric entries of /proc.
func procPids() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// procTasks lists the /proc directory of every task: each thread of each process.
func procTasks() ([]string, error) {
	pids, err := procPids()
	if err != nil {
		return nil, err
	}
	var tasks []string
	for _, pid := range pids {
		dir := fmt.Sprintf("/proc/%d", pid)
		tids, err := os.ReadDir(dir + "/task")
		if exited(dir, err) {
			continue
		} else if err != nil {
			return nil, err
		}
		for _, tid := range tids {
			tasks = append(tasks, dir+"/task/"+tid.Name())
		}
	}
	return tasks, nil
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
