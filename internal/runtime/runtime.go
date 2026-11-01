// Package runtime wraps an OCI runtime for engines without hook directories, such as Docker.
// On `create`, it adds the lanatt hooks to the bundle when the annotation is present.
package runtime

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"lanatt/internal/wire"
)

// Target picks the OCI runtime the wrapper becomes. LANATT_RUNTIME wins; otherwise the
// name after `lanatt-` in the command name: lanatt-runc runs runc, lanatt-crun runs crun.
// Docker cannot set a runtime's environment, so per-runtime symlinks are the Docker way.
func Target(argv0, env string) (string, error) {
	t := env
	if t == "" {
		t = strings.TrimPrefix(filepath.Base(argv0), "lanatt-")
	}
	switch {
	case t == "" || t == "lanatt" || strings.HasPrefix(t, "lanatt-"):
		return "", fmt.Errorf("no target runtime in %q", argv0)
	case filepath.IsAbs(t):
		return t, nil
	case strings.ContainsRune(t, '/'):
		return "", fmt.Errorf("runtime %q must be a bare name or an absolute path", t)
	default:
		return t, nil
	}
}

// IsWrapper reports whether the command was started as a lanatt-<runtime> symlink.
func IsWrapper(argv0 string) bool {
	base := filepath.Base(argv0)
	return strings.HasPrefix(base, "lanatt-") && len(base) > len("lanatt-")
}

// Bundle returns the bundle directory of a runc `create` call, or "" for any other call.
// cwd is the bundle when --bundle is absent, as in runc.
func Bundle(args []string, cwd string) string {
	create := -1
	for i, a := range args {
		if a == "create" {
			create = i
			break
		}
	}
	if create < 0 {
		return ""
	}
	rest := args[create+1:]
	for i, a := range rest {
		switch {
		case (a == "--bundle" || a == "-b") && i+1 < len(rest):
			return absolute(rest[i+1], cwd)
		case len(a) > 9 && a[:9] == "--bundle=":
			return absolute(a[9:], cwd)
		}
	}
	return cwd
}

func absolute(p, cwd string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// InjectHooks adds createRuntime and poststop hooks to an OCI config.
// It reports false, and leaves the config alone, when the annotation is absent.
func InjectHooks(config []byte, self, socket string) ([]byte, bool, error) {
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(config, &spec); err != nil {
		return nil, false, fmt.Errorf("config.json: %w", err)
	}
	var ann map[string]string
	if raw, ok := spec["annotations"]; ok {
		if err := json.Unmarshal(raw, &ann); err != nil {
			return nil, false, fmt.Errorf("config.json annotations: %w", err)
		}
	}
	if _, ok := ann[wire.AnnotationKey]; !ok {
		return config, false, nil
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := spec["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, false, fmt.Errorf("config.json hooks: %w", err)
		}
	}
	for stage, name := range map[string]string{"createRuntime": "create", "poststop": "stop"} {
		var list []map[string]any
		if raw, ok := hooks[stage]; ok {
			if err := json.Unmarshal(raw, &list); err != nil {
				return nil, false, fmt.Errorf("config.json hooks.%s: %w", stage, err)
			}
		}
		list = append(list, map[string]any{
			"path": self,
			"args": []string{"lanatt", "hook", name, "-socket", socket},
		})
		b, err := json.Marshal(list)
		if err != nil {
			return nil, false, err
		}
		hooks[stage] = b
	}
	b, err := json.Marshal(hooks)
	if err != nil {
		return nil, false, err
	}
	spec["hooks"] = b
	out, err := json.Marshal(spec)
	return out, true, err
}
