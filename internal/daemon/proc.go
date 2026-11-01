package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// target is the workload netns, held open so a reused pid cannot redirect the join.
type target struct {
	ns    *os.File
	netns uint64
}

// openTarget refuses a pid that sits in the daemon's netns, or that the caller does not own
// unless anyOwner is set.
func openTarget(uid uint32, pid int, anyOwner bool) (target, error) {
	if pid <= 0 {
		return target{}, errors.New("pid is missing")
	}
	before, err := startTime(pid)
	if err != nil {
		return target{}, err
	}
	ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return target{}, err
	}
	t, err := checkTarget(uid, pid, ns, before, anyOwner)
	if err != nil {
		ns.Close()
		return target{}, err
	}
	return t, nil
}

func checkTarget(uid uint32, pid int, ns *os.File, before string, anyOwner bool) (target, error) {
	owner, err := procUID(pid)
	if err != nil {
		return target{}, err
	}
	// Same start time after the open means the fd belongs to the process whose owner we read.
	if after, err := startTime(pid); err != nil || after != before {
		return target{}, fmt.Errorf("pid %d changed while being checked", pid)
	}
	if !anyOwner && owner != uid {
		return target{}, fmt.Errorf("pid %d is not owned by uid %d", pid, uid)
	}
	id, err := fileNsID(ns)
	if err != nil {
		return target{}, err
	}
	self, err := nsID("/proc/self/ns/net")
	if err != nil {
		return target{}, err
	}
	if id == self {
		return target{}, errors.New("target is the host netns")
	}
	return target{ns: ns, netns: id.ino}, nil
}

type nsKey struct{ dev, ino uint64 }

func nsID(path string) (nsKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nsKey{}, err
	}
	return statKey(path, fi)
}

func fileNsID(f *os.File) (nsKey, error) {
	fi, err := f.Stat()
	if err != nil {
		return nsKey{}, err
	}
	return statKey(f.Name(), fi)
}

func statKey(path string, fi os.FileInfo) (nsKey, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nsKey{}, fmt.Errorf("%s: no inode", path)
	}
	return nsKey{dev: uint64(st.Dev), ino: st.Ino}, nil
}

// procUID is the real host UID of pid.
func procUID(pid int) (uint32, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "Uid:" {
			uid, err := strconv.ParseUint(fields[1], 10, 32)
			return uint32(uid), err
		}
	}
	return 0, fmt.Errorf("pid %d: no Uid line", pid)
}

// startTime is field 22 of /proc/pid/stat; it tells a reused pid apart.
func startTime(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	s := string(data)
	end := strings.LastIndexByte(s, ')')
	if end < 0 {
		return "", fmt.Errorf("pid %d: bad stat", pid)
	}
	// Fields after the command start at field 3, so start time is index 19 here.
	fields := strings.Fields(s[end+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("pid %d: short stat", pid)
	}
	return fields[19], nil
}

// liveNetns is the inode of every netns some process is in.
func liveNetns() (map[uint64]bool, error) {
	paths, err := filepath.Glob("/proc/[0-9]*/ns/net")
	if err != nil {
		return nil, err
	}
	alive := map[uint64]bool{}
	for _, p := range paths {
		if id, err := nsID(p); err == nil {
			alive[id.ino] = true
		}
	}
	return alive, nil
}
