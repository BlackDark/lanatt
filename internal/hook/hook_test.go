package hook

import (
	"strings"
	"testing"

	"lanatt/internal/wire"
)

const state = `{"ociVersion":"1.0.2","id":"c1","status":"creating","pid":42,"bundle":"/b"}`

func bundle(annotations string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if path != "/b/config.json" {
			panic(path)
		}
		return []byte(`{"annotations":` + annotations + `}`), nil
	}
}

func TestNoAnnotationDoesNothing(t *testing.T) {
	for _, stage := range []string{"create", "stop"} {
		req, err := Request(stage, strings.NewReader(state), bundle(`{"other":"x"}`))
		if err != nil || req != nil {
			t.Errorf("%s: %+v %v", stage, req, err)
		}
	}
}

func TestCreateSendsJoins(t *testing.T) {
	ann := `{"io.lanatt.attachments":"[{\"network\":\"a\",\"address\":\"10.254.1.2\",\"mac\":\"02:00:00:00:00:02\",\"default\":true},{\"network\":\"b\",\"address\":\"10.254.2.2\",\"mac\":\"02:00:00:00:00:03\",\"interface\":\"eth9\"}]"}`
	req, err := Request("create", strings.NewReader(state), bundle(ann))
	if err != nil {
		t.Fatal(err)
	}
	if req.Op != wire.OpAttach || req.ID != "c1" || req.Pid != 42 || len(req.Joins) != 2 ||
		!req.Joins[0].Default || req.Joins[1].Interface != "eth9" {
		t.Fatalf("got %+v", req)
	}
}

func TestStopSendsDetach(t *testing.T) {
	req, err := Request("stop", strings.NewReader(state), bundle(`{"io.lanatt.attachments":"[]"}`))
	if err != nil || req.Op != wire.OpDetach || req.ID != "c1" {
		t.Fatalf("got %+v %v", req, err)
	}
}

func TestBadInput(t *testing.T) {
	if _, err := Request("create", strings.NewReader(state), bundle(`{"io.lanatt.attachments":"nope"}`)); err == nil {
		t.Error("bad annotation accepted")
	}
	misspelt := `{"io.lanatt.attachments":"[{\"network\":\"a\",\"address\":\"10.254.1.2\",\"ipv6\":\"fd00::2\"}]"}`
	if _, err := Request("create", strings.NewReader(state), bundle(misspelt)); err == nil {
		t.Error("unknown join field accepted")
	}
	if _, err := Request("start", strings.NewReader(state), bundle(`{}`)); err == nil {
		t.Error("unknown stage accepted")
	}
	if _, err := Request("create", strings.NewReader("{"), bundle(`{}`)); err == nil {
		t.Error("bad state accepted")
	}
}
