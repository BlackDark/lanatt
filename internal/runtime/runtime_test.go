package runtime

import (
	"encoding/json/v2"
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
		Process struct {
			Args []string `json:"args"`
		} `json:"process"`
		Hooks map[string][]struct {
			Path string   `json:"path"`
			Args []string `json:"args"`
		} `json:"hooks"`
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

func TestTarget(t *testing.T) {
	cases := []struct {
		argv0, env, want string
		ok               bool
	}{
		{"/usr/local/bin/lanatt-runc", "", "runc", true},
		{"lanatt-crun", "", "crun", true},
		{"/usr/local/bin/lanatt-runc", "crun", "crun", true},
		{"lanatt-runc", "/opt/bin/youki", "/opt/bin/youki", true},
		{"lanatt-runc", "bin/crun", "", false},
		{"lanatt-", "", "", false},
		{"lanatt-lanatt-runc", "", "", false},
		{"lanatt-runc", "lanatt", "", false},
	}
	for _, c := range cases {
		got, err := Target(c.argv0, c.env)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("Target(%q,%q)=%q,%v want %q ok=%v", c.argv0, c.env, got, err, c.want, c.ok)
		}
	}
}

func TestIsWrapper(t *testing.T) {
	for argv0, want := range map[string]bool{
		"/usr/local/bin/lanatt-runc": true,
		"lanatt-crun":                true,
		"/usr/local/bin/lanatt":      false,
		"lanatt-":                    false,
	} {
		if got := IsWrapper(argv0); got != want {
			t.Errorf("IsWrapper(%q)=%v", argv0, got)
		}
	}
}
