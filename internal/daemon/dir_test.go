package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareSocketDirAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run", "lanatt")
	if err := prepareSocketDir(filepath.Join(dir, "lanatt.sock")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o055 != 0o055 {
		t.Errorf("socket dir mode %v: hooks cannot traverse it", fi.Mode().Perm())
	}
}
