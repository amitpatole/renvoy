package plugin_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/amitpatole/renvoy/pkg/plugin"
)

func TestMessagesRoundTrip(t *testing.T) {
	req := plugin.Request{
		ID:   "1",
		Type: plugin.TypeHandshake,
		Handshake: &plugin.HandshakeRequest{
			CoreVersion:      "v0.1.0",
			ProtocolVersions: []string{plugin.ProtocolVersion},
			ConfigAPIVersion: "renvoy.dev/v1alpha1",
		},
	}
	resp := plugin.Response{
		ID:   "2",
		Type: plugin.TypeRender,
		Diagnostics: []plugin.Diagnostic{{
			Severity: "warning", Path: "stages[0].jobs[0]", Message: "m", Hint: "h",
		}},
		Files: []plugin.File{{Path: "ci/pipeline.yml", Content: "a: 1\n", Comment: "#"}},
	}
	for _, v := range []any{req, resp} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		back := reflect.New(reflect.TypeOf(v))
		if err := json.Unmarshal(b, back.Interface()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back.Elem().Interface(), v) {
			t.Fatalf("round trip changed %T:\n%s", v, b)
		}
	}
}

func TestWireNamesAreStable(t *testing.T) {
	b, err := json.Marshal(plugin.Request{ID: "1", Type: plugin.TypeRender, Render: &plugin.RenderOptions{SourceName: "renvoy.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"id":"1","type":"render","render":{"sourceName":"renvoy.yaml"}}`; got != want {
		t.Fatalf("wire format changed:\n got %s\nwant %s", got, want)
	}
}
