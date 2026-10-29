// Package api checks the API contract without serving it. The daemon's
// handlers are tested in internal/daemon; this test keeps api/openapi.yaml
// parseable and honest about the routes that package registers.
package api

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/shx-dow/lantern-go/internal/daemon"
)

const specPath = "openapi.yaml"

// httpMethods are the operation keys allowed on an OpenAPI path item.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// pathItemFields are the non-operation keys allowed on a path item.
var pathItemFields = map[string]bool{
	"$ref": true, "summary": true, "description": true,
	"servers": true, "parameters": true,
}

// readSpec parses the spec as a YAML document. Decoding into a Node (rather
// than a map) keeps every key so the duplicate check below can report both
// locations instead of silently keeping the last one.
func readSpec(t *testing.T) *yaml.Node {
	t.Helper()
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	if len(doc.Content) != 1 {
		t.Fatalf("%s: expected one document, got %d", specPath, len(doc.Content))
	}
	return doc.Content[0]
}

// TestSpecHasNoDuplicateKeys catches the failure YAML parsers hide: two
// mappings with the same key parse fine, and whichever one the reader keeps is
// an accident of the parser. This is the check that fails on the duplicate
// /v1/trust/{id} path.
func TestSpecHasNoDuplicateKeys(t *testing.T) {
	checkDuplicateKeys(t, readSpec(t), "root")
}

func checkDuplicateKeys(t *testing.T, n *yaml.Node, path string) {
	t.Helper()
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for i, c := range n.Content {
			checkDuplicateKeys(t, c, fmt.Sprintf("%s[%d]", path, i))
		}
	case yaml.MappingNode:
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if first, ok := seen[key.Value]; ok {
				t.Errorf("%s: duplicate key %q at line %d (first at line %d)", path, key.Value, key.Line, first)
			}
			seen[key.Value] = key.Line
			checkDuplicateKeys(t, value, path+"."+key.Value)
		}
	}
}

// TestSpecRefsResolve requires every local $ref to name something that
// exists. A broken ref is invisible at runtime until a client resolves it.
func TestSpecRefsResolve(t *testing.T) {
	root := readSpec(t)
	checkRefs(t, root, root, "root")
}

func checkRefs(t *testing.T, root, n *yaml.Node, path string) {
	t.Helper()
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Value == "$ref" && value.Kind == yaml.ScalarNode && strings.HasPrefix(value.Value, "#/") {
				if _, err := resolvePointer(root, value.Value); err != nil {
					t.Errorf("%s: %v", path, err)
				}
			}
		}
	}
	for _, c := range n.Content {
		checkRefs(t, root, c, path)
	}
}

func resolvePointer(root *yaml.Node, ref string) (*yaml.Node, error) {
	cur := root
	for _, raw := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		if cur.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("unresolved $ref %q: %q is not a mapping", ref, token)
		}
		var next *yaml.Node
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == token {
				next = cur.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("unresolved $ref %q", ref)
		}
		cur = next
	}
	return cur, nil
}

// TestSpecOperationsHaveResponses walks every path item and requires each
// operation to say what it returns. OpenAPI treats a missing responses map as
// invalid, and a client generator treats it as "unknown".
func TestSpecOperationsHaveResponses(t *testing.T) {
	checkOperations(t, readSpec(t))
}

func checkOperations(t *testing.T, root *yaml.Node) {
	t.Helper()
	paths := mappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		t.Fatalf("%s: no paths mapping", specPath)
	}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		if !strings.HasPrefix(path, "/") {
			t.Errorf("paths: key %q is not a path", path)
			continue
		}
		if item.Kind != yaml.MappingNode {
			t.Errorf("paths %s: not a mapping", path)
			continue
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			method, op := item.Content[j].Value, item.Content[j+1]
			if !httpMethods[method] {
				if !pathItemFields[method] {
					t.Errorf("paths %s: unexpected path-item key %q", path, method)
				}
				continue
			}
			if op.Kind != yaml.MappingNode {
				t.Errorf("%s %s: operation is not a mapping", strings.ToUpper(method), path)
				continue
			}
			responses := mappingValue(op, "responses")
			if responses == nil || responses.Kind != yaml.MappingNode || len(responses.Content) == 0 {
				t.Errorf("%s %s: operation has no responses", strings.ToUpper(method), path)
			}
		}
	}
}

// TestSpecMatchesDaemonRoutes requires the spec and the mux to serve the same
// set of method and path pairs. Changing one without the other is drift that
// no schema check can see.
func TestSpecMatchesDaemonRoutes(t *testing.T) {
	root := readSpec(t)
	paths := mappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		t.Fatalf("%s: no paths mapping", specPath)
	}

	spec := make(map[string]bool)
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		for j := 0; j+1 < len(item.Content); j += 2 {
			if httpMethods[item.Content[j].Value] {
				spec[strings.ToUpper(item.Content[j].Value)+" "+path] = true
			}
		}
	}

	served := make(map[string]bool)
	for _, r := range (&daemon.Handler{}).RouteTable() {
		served[r.Method+" "+r.Pattern] = true
	}

	for _, route := range keyDiff(spec, served) {
		t.Errorf("in %s but not served by the daemon: %s", specPath, route)
	}
	for _, route := range keyDiff(served, spec) {
		t.Errorf("served by the daemon but missing from %s: %s", specPath, route)
	}
}

// keyDiff returns the sorted keys in a that are absent from b.
func keyDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
