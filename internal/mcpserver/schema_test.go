package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// Flag properties are declared as booleans. A schema that says "string" for a
// flag invites a model to send "no" or "off", which read as false with no
// complaint — so the declared type is what stops the mistake happening, and it
// is worth pinning.
func TestFlagSchemasDeclareBooleans(t *testing.T) {
	for _, def := range tools() {
		schema, ok := def.InputSchema.(map[string]any)
		if !ok {
			t.Errorf("%s inputSchema is not an object: %#v", def.Name, def.InputSchema)
			continue
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range []string{"overwrite", "probe"} {
			raw, present := props[name]
			if !present {
				continue
			}
			prop, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s.%s schema is not an object: %#v", def.Name, name, raw)
				continue
			}
			if prop["type"] != "boolean" {
				t.Errorf("%s.%s is declared %v, want boolean", def.Name, name, prop["type"])
			}
		}
	}
}

// isTruthyArg accepts the shapes clients actually send for a flag. Anything
// unrecognised is false, because a flag that was not clearly requested must not
// replace a file.
func TestIsTruthyArgAcceptsWhatClientsSend(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string True", "True", true},
		{"string yes", "yes", true},
		{"string 1", "1", true},
		{"number 1", float64(1), true},
		{"number 0", float64(0), false},
		// The silent-wrong-answer cases: these read as "not requested" rather
		// than erroring, which is safe for overwrite and wrong for anything else.
		{"string no", "no", false},
		{"string off", "off", false},
		{"string 0", "0", false},
		{"empty string", "", false},
		{"nil", nil, false},
		{"unknown", "maybe", false},
	} {
		if got := isTruthyArg(tc.in); got != tc.want {
			t.Errorf("%s: isTruthyArg(%#v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// The push tool must forward overwrite as a real boolean to the daemon, whatever
// shape the client sent. It previously parsed booleans and "true"/"1"/"yes" only,
// so a client sending 1 silently got overwrite=false and the push was refused.
func TestPushForwardsOverwriteAsBool(t *testing.T) {
	for _, tc := range []struct {
		name string
		send any
		want bool
	}{
		{"absent", nil, false},
		{"bool true", true, true},
		{"string true", "true", true},
		{"number 1", float64(1), true},
		{"string no", "no", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got any
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/pushes", func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				got = body["overwrite"]
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			s, buf := testServer(t, lanternclient.New(srv.URL, ""))
			args := map[string]any{"to": "nas", "path": "/tmp/a.txt"}
			if tc.send != nil {
				args["overwrite"] = tc.send
			}
			params, _ := json.Marshal(map[string]any{"name": "push", "arguments": args})
			s.handle(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})

			if lastResponse(t, buf).Error != nil {
				t.Fatalf("tools/call errored: %+v", lastResponse(t, buf).Error)
			}
			if got != tc.want {
				t.Errorf("daemon received overwrite=%#v (%T), want %v", got, got, tc.want)
			}
		})
	}
}
