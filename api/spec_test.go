package api

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

var methods = []string{"get", "put", "post", "delete", "patch"}

var accessMarks = []string{"public", "user", "scoped", "listing", "admin"}

type op struct {
	Method, Path, ID, Access string
	Params                   []map[string]any
	Body                     map[string]any
}

func operations(t *testing.T) []op {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(Spec, &doc); err != nil {
		t.Fatal(err)
	}
	var out []op
	for p, item := range doc.Paths {
		for m, raw := range item {
			if !slices.Contains(methods, m) {
				continue
			}
			o := op{Method: m, Path: p}
			o.ID, _ = raw["operationId"].(string)
			o.Access, _ = raw["x-hogwatch-access"].(string)
			if ps, ok := raw["parameters"].([]any); ok {
				for _, x := range ps {
					if mm, ok := x.(map[string]any); ok {
						o.Params = append(o.Params, mm)
					}
				}
			}
			if rb, ok := raw["requestBody"].(map[string]any); ok {
				o.Body = rb
			}
			out = append(out, o)
		}
	}
	return out
}

func TestSpec_EveryOperationHasAccess(t *testing.T) {
	seen := map[string]bool{}
	ops := operations(t)
	if len(ops) < 50 {
		t.Fatalf("only %d operations", len(ops))
	}
	for _, o := range ops {
		if !slices.Contains(accessMarks, o.Access) {
			t.Errorf("%s %s: x-hogwatch-access %q", o.Method, o.Path, o.Access)
		}
		if o.ID == "" || seen[o.ID] {
			t.Errorf("%s %s: operationId %q missing or duplicate", o.Method, o.Path, o.ID)
		}
		seen[o.ID] = true
	}
}

// Invariant 3: only GET /resolve takes a host path.
func TestSpec_NoPathParameters(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(Spec, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(v any, where string)
	walk = func(v any, where string) {
		switch x := v.(type) {
		case map[string]any:
			if props, ok := x["properties"].(map[string]any); ok {
				if _, bad := props["path"]; bad {
					t.Errorf("%s: property named path", where)
				}
			}
			for k, c := range x {
				walk(c, where+"/"+k)
			}
		case []any:
			for _, c := range x {
				walk(c, where)
			}
		}
	}
	walk(doc["components"], "components")
	for _, o := range operations(t) {
		for _, p := range o.Params {
			if p["name"] == "path" && (o.Method != "get" || o.Path != "/api/v1/resolve") {
				t.Errorf("%s %s: parameter named path", o.Method, o.Path)
			}
		}
		if o.Body != nil {
			walk(o.Body, o.ID)
		}
	}
}

func TestSpec_AccessMatchesSpecTable(t *testing.T) {
	want := map[string]string{
		"healthz": "public", "readyz": "public", "login": "public", "setup": "public",
		"getSetupStatus": "public", "oidcCallback": "public", "getAuthConfig": "public",
		"logout": "user", "getMe": "user", "dismissNotice": "user", "changePassword": "user", "getMyUsage": "user",
		"listRoots": "scoped", "resolve": "scoped", "getNode": "scoped", "listChildren": "scoped",
		"exportNode": "scoped", "listScans": "scoped",
		"createListing": "listing", "getListing": "listing", "cancelListing": "listing",
		"listUsers": "admin", "createScan": "admin", "getScanProgress": "admin",
		"listAudit": "admin", "getSettings": "admin", "createUserPath": "admin",
	}
	got := map[string]string{}
	for _, o := range operations(t) {
		got[o.ID] = o.Access
	}
	for id, a := range want {
		if got[id] != a {
			t.Errorf("%s: access %q, want %q", id, got[id], a)
		}
	}
}

// updateRoot takes a patch: every field optional, and no container_path (a
// root's mount cannot change; delete and recreate).
func TestSpec_UpdateRootUsesPatch(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(Spec, &doc); err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, o := range operations(t) {
		if o.ID != "updateRoot" {
			continue
		}
		c, _ := o.Body["content"].(map[string]any)
		j, _ := c["application/json"].(map[string]any)
		s, _ := j["schema"].(map[string]any)
		ref, _ = s["$ref"].(string)
	}
	if ref != "#/components/schemas/RootPatch" {
		t.Fatalf("updateRoot request schema = %q, want RootPatch", ref)
	}
	patch := doc.Components.Schemas["RootPatch"]
	if _, ok := patch["required"]; ok {
		t.Error("RootPatch has required fields")
	}
	props, _ := patch["properties"].(map[string]any)
	if _, ok := props["container_path"]; ok {
		t.Error("RootPatch has container_path")
	}
}
