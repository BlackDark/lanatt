package runtime

import (
	"encoding/json"
	"testing"
)

func TestBundle(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--root", "/r", "--log", "/l", "create", "--bundle", "/b", "--pid-file", "/p", "id"}, "/b"},
		{[]string{"create", "-b", "rel", "id"}, "/cwd/rel"},
		{[]string{"create", "--bundle=/b2", "id"}, "/b2"},
		{[]string{"create", "id"}, "/cwd"},
		{[]string{"--root", "/r", "start", "id"}, ""},
		{[]string{"delete", "--bundle", "/b", "id"}, ""},
	}
	for _, c := range cases {
		if got := Bundle(c.args, "/cwd"); got != c.want {
			t.Errorf("Bundle(%v)=%q want %q", c.args, got, c.want)
		}
	}
}

func TestInjectHooksWithoutAnnotation(t *testing.T) {
	in := []byte(`{"ociVersion":"1.0.2","annotations":{"other":"x"}}`)
	out, changed, err := InjectHooks(in, "/usr/local/bin/lanatt", "/run/lanatt/lanatt.sock")
	if err != nil || changed || string(out) != string(in) {
		t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
	}
}

func TestInjectHooksKeepsSpecAndExistingHooks(t *testing.T) {
	in := []byte(`{"ociVersion":"1.0.2","process":{"args":["nginx"]},
		"annotations":{"io.lanatt.attachments":"[]"},
		"hooks":{"prestart":[{"path":"/a"}],"poststop":[{"path":"/b"}]}}`)
	out, changed, err := InjectHooks(in, "/usr/local/bin/lanatt", "/s")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var spec struct {
		Process struct{ Args []string }
		Hooks   map[string][]struct {
			Path string
			Args []string
		}
	}
	if err := json.Unmarshal(out, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Process.Args) != 1 || len(spec.Hooks["prestart"]) != 1 {
		t.Fatalf("spec lost fields: %s", out)
	}
	cr, ps := spec.Hooks["createRuntime"], spec.Hooks["poststop"]
	if len(cr) != 1 || cr[0].Path != "/usr/local/bin/lanatt" || cr[0].Args[2] != "create" || cr[0].Args[4] != "/s" {
		t.Fatalf("createRuntime: %+v", cr)
	}
	if len(ps) != 2 || ps[0].Path != "/b" || ps[1].Args[2] != "stop" {
		t.Fatalf("poststop: %+v", ps)
	}
}

func TestInjectHooksBadConfig(t *testing.T) {
	if _, _, err := InjectHooks([]byte(`{`), "/x", "/s"); err == nil {
		t.Error("bad JSON accepted")
	}
}
