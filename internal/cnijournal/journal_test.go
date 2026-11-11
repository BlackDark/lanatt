package cnijournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeRunner records every call. Errors are set per test to inject failures.
type fakeRunner struct {
	adds      []string
	dels      []Record
	goneFlags []bool
	addErr    error
	delErr    error
	// onAdd runs inside ADD, after the plugin would have done its work; onDel runs inside DEL.
	onAdd func()
	onDel func(Record)
}

func (f *fakeRunner) Add(_ context.Context, r Record) (json.RawMessage, error) {
	f.adds = append(f.adds, r.ContainerID)
	if f.onAdd != nil {
		f.onAdd()
	}
	if f.addErr != nil {
		return nil, f.addErr
	}
	return json.RawMessage(`{"cniVersion":"1.0.0"}`), nil
}

func (f *fakeRunner) Del(_ context.Context, r Record, gone bool) error {
	f.dels = append(f.dels, r)
	if f.onDel != nil {
		f.onDel(r)
	}
	f.goneFlags = append(f.goneFlags, gone)
	return f.delErr
}

func rec(uid uint32, id string) Record {
	return Record{UID: uid, ContainerID: id, Interface: "lan0", Network: "lan",
		Plugin: "macvlan", PluginVersion: "1.1.1", Config: json.RawMessage(`{"type":"macvlan"}`)}
}

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// reopen simulates a restart: a fresh store over the same directory.
func reopen(t *testing.T, s *Store) *Store {
	t.Helper()
	return open(t, s.dir)
}

func mustGet(t *testing.T, s *Store, uid uint32, id string) (Record, bool) {
	t.Helper()
	r, ok, err := s.Get(uid, id)
	if err != nil {
		t.Fatal(err)
	}
	return r, ok
}

func noneGone(Record) bool { return false }

// sameJSON compares ignoring the indentation the store adds.
func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && x.String() == y.String()
}

func TestCNIIDStableScopedAndCharset(t *testing.T) {
	if CNIID(1000, "c") != CNIID(1000, "c") {
		t.Error("not stable")
	}
	if CNIID(1000, "c") == CNIID(1001, "c") {
		t.Error("same id for two uids")
	}
	if !regexp.MustCompile(`^lt[0-9a-f]{32}$`).MatchString(CNIID(1000, "c")) {
		t.Errorf("charset: %s", CNIID(1000, "c"))
	}
}

func TestAttachCommitsResult(t *testing.T) {
	s, run := open(t, t.TempDir()), &fakeRunner{}
	res, err := s.Attach(context.Background(), run, rec(1000, "c1"))
	if err != nil || string(res) != `{"cniVersion":"1.0.0"}` {
		t.Fatalf("attach: %v %s", err, res)
	}
	r, ok := mustGet(t, s, 1000, "c1")
	if !ok || r.State != StateAdded || !sameJSON(r.Result, res) {
		t.Errorf("record: %+v", r)
	}
	if len(run.dels) != 0 {
		t.Errorf("DEL on success: %v", run.dels)
	}
}

func TestAttachAddFailureRunsDel(t *testing.T) {
	s, run := open(t, t.TempDir()), &fakeRunner{addErr: errors.New("boom")}
	if _, err := s.Attach(context.Background(), run, rec(1000, "c1")); err == nil {
		t.Fatal("ADD error not returned")
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept after DEL succeeded")
	}
	if len(run.dels) != 1 || run.goneFlags[0] {
		t.Errorf("dels %v gone %v", run.dels, run.goneFlags)
	}
	if string(run.dels[0].Config) != `{"type":"macvlan"}` {
		t.Errorf("DEL without stored config: %s", run.dels[0].Config)
	}
}

func TestAttachDelFailureKeepsRecord(t *testing.T) {
	s, run := open(t, t.TempDir()), &fakeRunner{addErr: errors.New("boom"), delErr: errors.New("plugin down")}
	if _, err := s.Attach(context.Background(), run, rec(1000, "c1")); err == nil {
		t.Fatal("expected error")
	}
	r, ok := mustGet(t, s, 1000, "c1")
	if !ok || r.State != StateCleanup {
		t.Fatalf("want cleanup record, got %+v ok=%v", r, ok)
	}
}

func TestRepeatAddRefused(t *testing.T) {
	s, run := open(t, t.TempDir()), &fakeRunner{}
	if _, err := s.Attach(context.Background(), run, rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(rec(1000, "c1")); !errors.Is(err, ErrExists) {
		t.Errorf("second Begin: %v", err)
	}
	if len(run.adds) != 1 {
		t.Errorf("ADD ran %d times", len(run.adds))
	}
}

func TestCrashAfterIntentReconcilesWithDel(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.Begin(rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := reopen(t, s).Reconcile(context.Background(), run, noneGone); err != nil {
		t.Fatal(err)
	}
	if len(run.adds) != 0 || len(run.dels) != 1 {
		t.Errorf("adds %v dels %v", run.adds, run.dels)
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept after reconcile")
	}
}

func TestCrashAfterAddBeforeCommitReconcilesWithDel(t *testing.T) {
	s := open(t, t.TempDir())
	r, err := s.Begin(rec(1000, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if _, err := run.Add(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// Crash here: ADD ran, no result saved.
	if err := reopen(t, s).Reconcile(context.Background(), run, noneGone); err != nil {
		t.Fatal(err)
	}
	if len(run.adds) != 1 || len(run.dels) != 1 {
		t.Errorf("adds %v dels %v: ADD must not repeat", run.adds, run.dels)
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept after reconcile")
	}
}

// A commit that cannot write must not leave ADD without DEL: DEL is attempted at once and
// the record survives until a later reconcile can finish it.
func TestCommitFailureLeavesOwedDel(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission")
	}
	dir := t.TempDir()
	s := open(t, dir)
	run := &fakeRunner{onAdd: func() { os.Chmod(dir, 0o500) }}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := s.Attach(context.Background(), run, rec(1000, "c1")); err == nil {
		t.Fatal("commit failure not returned")
	}
	if len(run.dels) != 1 {
		t.Fatalf("ADD ran without an immediate DEL: %v", run.dels)
	}
	os.Chmod(dir, 0o700)
	recovered := &fakeRunner{}
	if err := reopen(t, s).Reconcile(context.Background(), recovered, noneGone); err != nil {
		t.Fatal(err)
	}
	if len(run.dels) != 1 || len(run.dels[0].Result) == 0 {
		t.Errorf("rollback DEL lost the ADD result: %+v", run.dels)
	}
	if len(recovered.dels) != 1 || len(recovered.dels[0].Result) != 0 {
		t.Errorf("reconcile DEL: want one DEL without a stored result: %+v", recovered.dels)
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept after recovery")
	}
}

func TestCrashAfterAddedKeepsLiveAttachment(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.Attach(context.Background(), &fakeRunner{}, rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := reopen(t, s).Reconcile(context.Background(), run, noneGone); err != nil {
		t.Fatal(err)
	}
	if len(run.dels) != 0 {
		t.Error("live attachment torn down at reconcile")
	}
	if _, ok := mustGet(t, s, 1000, "c1"); !ok {
		t.Error("live record lost")
	}
	gone := func(Record) bool { return true }
	if err := reopen(t, s).Reconcile(context.Background(), run, gone); err != nil {
		t.Fatal(err)
	}
	if len(run.dels) != 1 || !run.goneFlags[0] {
		t.Errorf("netns-gone added record: dels %v gone %v", run.dels, run.goneFlags)
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept after DEL")
	}
}

func TestDelRetriedUntilItSucceeds(t *testing.T) {
	s := open(t, t.TempDir())
	failing := &fakeRunner{addErr: errors.New("boom"), delErr: errors.New("plugin down")}
	s.Attach(context.Background(), failing, rec(1000, "c1"))
	if err := reopen(t, s).Reconcile(context.Background(), failing, noneGone); err == nil {
		t.Fatal("reconcile hid a failed DEL")
	}
	if _, ok := mustGet(t, s, 1000, "c1"); !ok {
		t.Fatal("record dropped while DEL failed")
	}
	if err := reopen(t, s).Reconcile(context.Background(), &fakeRunner{}, noneGone); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept after DEL recovered")
	}
}

func TestReleaseMissingNetnsStillDels(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.Attach(context.Background(), &fakeRunner{}, rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := s.Release(context.Background(), run, 1000, "c1", true); err != nil {
		t.Fatal(err)
	}
	if len(run.dels) != 1 || !run.goneFlags[0] {
		t.Errorf("dels %v gone %v", run.dels, run.goneFlags)
	}
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record kept")
	}
}

func TestUIDCannotTouchAnotherUIDsRecord(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.Attach(context.Background(), &fakeRunner{}, rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := s.Release(context.Background(), run, 1001, "c1", false); err != nil {
		t.Fatal(err)
	}
	if len(run.dels) != 0 {
		t.Error("uid 1001 released uid 1000's attachment")
	}
	if _, ok := mustGet(t, s, 1001, "c1"); ok {
		t.Error("uid 1001 sees uid 1000's record")
	}
	if _, ok := mustGet(t, s, 1000, "c1"); !ok {
		t.Error("owner's record gone")
	}
}

func TestCorruptRecordReportedNotDeleted(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	bad := filepath.Join(dir, "1000-ltdeadbeef.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	err := s.Reconcile(context.Background(), run, noneGone)
	if err == nil || !strings.Contains(err.Error(), "ltdeadbeef") {
		t.Errorf("corrupt file not reported: %v", err)
	}
	if len(run.dels) != 1 {
		t.Errorf("good record not processed: %v", run.dels)
	}
	if _, statErr := os.Stat(bad); statErr != nil {
		t.Error("corrupt file removed")
	}
}

func TestMisnamedRecordReported(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	r := rec(1000, "c1")
	r.CNIID = CNIID(1000, "c1")
	r.State = StateAdded
	data, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(dir, "1000-ltwrong.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := s.Reconcile(context.Background(), run, noneGone); err == nil {
		t.Error("misnamed record not reported")
	}
	if len(run.dels) != 0 {
		t.Errorf("misnamed record used: %v", run.dels)
	}
}

func TestOpenTightensExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	os.Mkdir(dir, 0o755)
	open(t, dir)
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
}

func TestRecordFilesPrivate(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.Begin(rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", e.Name(), fi.Mode().Perm())
		}
	}
}

func TestInvalidStateRejected(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	r := rec(1000, "c1")
	r.CNIID = CNIID(1000, "c1")
	r.State = State("adedd")
	data, _ := json.Marshal(r)
	p := filepath.Join(dir, "1000-"+r.CNIID+".json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := s.Reconcile(context.Background(), run, noneGone); err == nil {
		t.Error("unknown state not reported")
	}
	if len(run.dels) != 0 {
		t.Errorf("record with unknown state used: %v", run.dels)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("record with unknown state removed")
	}
}

// A release of a live attachment must persist the owed DEL before DEL runs.
func TestReleaseMarksCleanupBeforeDel(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.Attach(context.Background(), &fakeRunner{}, rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	var seen State
	run := &fakeRunner{onDel: func(Record) {
		r, _, _ := s.Get(1000, "c1")
		seen = r.State
	}}
	if err := s.Release(context.Background(), run, 1000, "c1", false); err != nil {
		t.Fatal(err)
	}
	if seen != StateCleanup {
		t.Errorf("state during DEL: %q, want cleanup", seen)
	}
}

func TestOpenRejectsGroupWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	os.Mkdir(dir, 0o777)
	os.Chmod(dir, 0o777)
	if _, err := Open(dir); err == nil {
		t.Error("group/other-writable journal accepted")
	}
}

// Concurrent release and reconcile of one attachment must not resurrect or double-free it.
func TestConcurrentReleaseAndReconcile(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.Attach(context.Background(), &fakeRunner{}, rec(1000, "c1")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Release(context.Background(), &fakeRunner{}, 1000, "c1", false)
		}()
		go func() {
			defer wg.Done()
			s.Reconcile(context.Background(), &fakeRunner{}, noneGone)
		}()
	}
	wg.Wait()
	if _, ok := mustGet(t, s, 1000, "c1"); ok {
		t.Error("record survived release")
	}
}
