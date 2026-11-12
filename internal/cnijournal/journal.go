// Package cnijournal keeps a durable record of each CNI attachment so a crash never leaves
// an ADD without a DEL. It runs no plugin: the caller supplies the Runner.
package cnijournal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// State is where an attachment stands in its lifecycle.
type State string

const (
	// StateIntent: ADD may have run, so DEL is owed.
	StateIntent State = "intent"
	// StateAdded: ADD succeeded and its result is saved.
	StateAdded State = "added"
	// StateCleanup: ADD failed or was interrupted, so DEL is owed.
	StateCleanup State = "cleanup"
)

// ErrExists means the caller already has a record for this container.
var ErrExists = errors.New("attachment already recorded")

// Record is one attachment. Config and Result are kept verbatim so DEL can run without
// the original request.
type Record struct {
	UID           uint32            `json:"uid"`
	ContainerID   string            `json:"container_id"`
	CNIID         string            `json:"cni_id"`
	Interface     string            `json:"interface"`
	Network       string            `json:"network"`
	Plugin        string            `json:"plugin"`
	PluginVersion string            `json:"plugin_version"`
	Config        jsontext.Value    `json:"config"`
	Args          map[string]string `json:"args,omitempty"`
	State         State             `json:"state"`
	Result        jsontext.Value    `json:"result,omitempty"`
}

// Runner executes CNI ADD and DEL. Add must return an error for a malformed result.
// Del must succeed when the netns is gone and when the attachment is already removed.
type Runner interface {
	Add(ctx context.Context, r Record) (jsontext.Value, error)
	Del(ctx context.Context, r Record, netnsGone bool) error
}

// CNIID derives the CNI container id. Hashing keeps it in the permitted charset and keeps
// one UID from naming another UID's attachment.
func CNIID(uid uint32, containerID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", uid, containerID)))
	return "lt" + hex.EncodeToString(sum[:])[:32]
}

// Store is a directory of records, one file each. Every operation takes the store lock.
type Store struct {
	mu    sync.Mutex
	dir   string
	locks map[string]*keyLock
}

// Open creates dir if needed. An existing dir must be a real directory owned by the caller
// and not writable by others; it is then made private. A new dir's entry is synced to its
// parent, or a crash after ADD could leave no record to undo it.
func Open(dir string) (*Store, error) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return nil, err
		}
		if fi, err = os.Lstat(dir); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("journal %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("journal %s owned by uid %d, want %d", dir, st.Uid, os.Geteuid())
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("journal %s is group or other writable", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, locks: map[string]*keyLock{}}, nil
}

// keyLock is one attachment's lock; refs lets the map drop it once nobody waits.
type keyLock struct {
	m    sync.Mutex
	refs int
}

// lock serialises whole lifecycles of one attachment: its store reads, runner calls and
// record writes. It is taken before the store mutex is held, never inside it.
func (s *Store) lock(uid uint32, containerID string) func() {
	k := CNIID(uid, containerID)
	s.mu.Lock()
	kl := s.locks[k]
	if kl == nil {
		kl = &keyLock{}
		s.locks[k] = kl
	}
	kl.refs++
	s.mu.Unlock()
	kl.m.Lock()
	return func() {
		kl.m.Unlock()
		s.mu.Lock()
		kl.refs--
		if kl.refs == 0 {
			delete(s.locks, k)
		}
		s.mu.Unlock()
	}
}

// Get returns uid's record for containerID; ok is false when there is none.
func (s *Store) Get(uid uint32, containerID string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(uid, CNIID(uid, containerID))
	r, ok, err := read(p)
	if err != nil || !ok {
		return r, ok, err
	}
	return r, true, validate(p, r)
}

// Begin records intent before ADD runs. It refuses when a record already exists.
func (s *Store) Begin(r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.CNIID = CNIID(r.UID, r.ContainerID)
	r.State = StateIntent
	r.Result = nil
	p := s.path(r.UID, r.CNIID)
	_, ok, err := read(p)
	if err != nil {
		return Record{}, err
	}
	if ok {
		return Record{}, ErrExists
	}
	return r, write(p, r)
}

// commit saves result and marks the attachment added. Call it before acknowledging ADD.
func (s *Store) commit(uid uint32, containerID string, result jsontext.Value) error {
	return s.update(uid, containerID, []State{StateIntent}, func(r *Record) {
		r.State = StateAdded
		r.Result = result
	})
}

// fail marks an ADD that ran, or may have, as owing DEL. A committed record can still fail
// here if the write that followed its save was interrupted.
func (s *Store) fail(uid uint32, containerID string) error {
	return s.update(uid, containerID, []State{StateIntent, StateAdded}, func(r *Record) {
		r.State = StateCleanup
	})
}

// remove deletes r's record. Call it only after DEL succeeded.
func (s *Store) remove(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(r.UID, r.CNIID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Without the sync, a crash could bring back an added record whose DEL already ran.
	return syncDir(s.dir)
}

// list returns every readable record. A file whose name does not match its contents is
// an error, so it is never removed under the wrong name.
func (s *Store) list() ([]Record, []error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, []error{err}
	}
	var out []Record
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(s.dir, e.Name())
		r, ok, err := read(p)
		if err == nil && ok {
			err = validate(p, r)
		}
		switch {
		case err != nil:
			errs = append(errs, err)
		case ok:
			out = append(out, r)
		}
	}
	return out, errs
}

func (s *Store) path(uid uint32, cniID string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d-%s.json", uid, cniID))
}

// update changes uid's record only if it is in one of the states from.
func (s *Store) update(uid uint32, containerID string, from []State, f func(*Record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(uid, CNIID(uid, containerID))
	r, ok, err := read(p)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no attachment for container %s", containerID)
	}
	if !slices.Contains(from, r.State) {
		return fmt.Errorf("container %s is %s, not one of %v", containerID, r.State, from)
	}
	f(&r)
	return write(p, r)
}

// validate rejects a record stored under a name its contents do not imply, or in an unknown
// state. Reconcile would otherwise act on it.
func validate(path string, r Record) error {
	if filepath.Base(path) != fmt.Sprintf("%d-%s.json", r.UID, r.CNIID) ||
		r.CNIID != CNIID(r.UID, r.ContainerID) {
		return fmt.Errorf("record %s does not match its name", path)
	}
	switch r.State {
	case StateIntent, StateAdded, StateCleanup:
		return nil
	}
	return fmt.Errorf("record %s has unknown state %q", path, r.State)
}

func read(path string) (Record, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, fmt.Errorf("record %s: %w", path, err)
	}
	return r, true, nil
}

// write replaces path atomically: the file and its directory are synced before success.
func write(path string, r Record) error {
	data, err := json.Marshal(r, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
