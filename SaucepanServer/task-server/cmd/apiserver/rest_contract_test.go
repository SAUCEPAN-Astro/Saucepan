package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The REST contract single-source-of-truth gate (#452).
//
//   - TestRoutesJSONMatchesTable: contracts/rest/routes.json must equal the
//     apiRoutes table (+ the two health routes). Drift in either direction
//     fails the build, so routes.json can be trusted by the SDK's own gate
//     (SaucepanSDK/python/tests/test_rest_contract.py).
//   - TestOpenAPIPathsAreRealRoutes: every path/method in
//     SaucepanSDK/openapi.yaml must resolve to a registered route. Catches the
//     spec drifting into fiction (its whole value is being the artifact an
//     external developer trusts).

const routesJSONPath = "../../contracts/rest/routes.json"

// openapi.yaml lives in the SDK tree; from cmd/apiserver/ that is four levels up.
const openapiPath = "../../../../SaucepanSDK/openapi.yaml"

type contractRoute struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Surface string `json:"surface"`
}

func loadRoutesJSON(t *testing.T) []contractRoute {
	t.Helper()
	raw, err := os.ReadFile(routesJSONPath)
	if err != nil {
		t.Fatalf("read %s: %v", routesJSONPath, err)
	}
	var doc struct {
		Routes []contractRoute `json:"routes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", routesJSONPath, err)
	}
	return doc.Routes
}

// tableRoutes is apiRoutes plus the two health endpoints registerAPIRoutes
// wires directly — the full registered set, as contractRoutes.
func tableRoutes() []contractRoute {
	out := make([]contractRoute, 0, len(apiRoutes)+2)
	for _, r := range apiRoutes {
		out = append(out, contractRoute{r.Method, r.Path, string(r.Surface)})
	}
	out = append(out,
		contractRoute{"GET", "/", "infra"},
		contractRoute{"GET", "/cohort/status", "infra"},
	)
	return out
}

func key(r contractRoute) string { return r.Method + " " + r.Path + " (" + r.Surface + ")" }

func TestRoutesJSONMatchesTable(t *testing.T) {
	got := map[string]bool{}
	for _, r := range tableRoutes() {
		if got[key(r)] {
			t.Errorf("duplicate route in apiRoutes: %s", key(r))
		}
		got[key(r)] = true
	}
	want := map[string]bool{}
	for _, r := range loadRoutesJSON(t) {
		if want[key(r)] {
			t.Errorf("duplicate route in routes.json: %s", key(r))
		}
		want[key(r)] = true
	}

	for k := range got {
		if !want[k] {
			t.Errorf("route in the Go table but not in routes.json: %s\n\trun: python3 contracts/rest/gen.py", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("route in routes.json but not in the Go table: %s\n\trun: python3 contracts/rest/gen.py", k)
		}
	}
}

// normPath collapses every {param} segment to {} so path templates compare
// regardless of the parameter name the Go route and the spec each chose.
func normPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

func TestOpenAPIPathsAreRealRoutes(t *testing.T) {
	raw, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatalf("required openapi.yaml missing at %s: %v", openapiPath, err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("openapi.yaml declares no paths")
	}

	// Registered routes, normalised, in both bare and /api/v1-prefixed form
	// (openapi.yaml's servers.url ends in /api/v1, but it lists a few
	// host-relative /auth/* paths too).
	registered := map[string]bool{}
	for _, r := range tableRoutes() {
		np := normPath(r.Path)
		registered[r.Method+" "+np] = true
		registered[r.Method+" "+strings.TrimPrefix(np, "/api/v1")] = true
	}

	httpMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true, "delete": true,
	}
	var missing []string
	for p, ops := range spec.Paths {
		for m := range ops {
			if !httpMethods[strings.ToLower(m)] {
				continue // parameters:, description:, $ref:, ...
			}
			method := strings.ToUpper(m)
			np := normPath(p)
			if registered[method+" "+np] || registered[method+" "+"/api/v1"+np] {
				continue
			}
			missing = append(missing, method+" "+p)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("openapi.yaml declares %s, which is not a registered apiserver route", m)
	}
}

// TestDeveloperSurfaceIsFullyInOpenAPI is the other direction: every route
// tagged surfaceDeveloper is part of the published API and must appear in
// openapi.yaml. A new developer endpoint that skips the spec fails here.
func TestDeveloperSurfaceIsFullyInOpenAPI(t *testing.T) {
	raw, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatalf("required openapi.yaml missing at %s: %v", openapiPath, err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}

	specSet := map[string]bool{}
	for p, ops := range spec.Paths {
		np := normPath(p)
		for m := range ops {
			method := strings.ToUpper(m)
			specSet[method+" "+np] = true
			specSet[method+" "+strings.TrimPrefix(np, "/api/v1")] = true
		}
	}

	for _, r := range apiRoutes {
		if r.Surface != surfaceDeveloper {
			continue
		}
		np := normPath(r.Path)
		if specSet[r.Method+" "+np] || specSet[r.Method+" "+strings.TrimPrefix(np, "/api/v1")] {
			continue
		}
		t.Errorf("developer-surface route %s %s is missing from openapi.yaml", r.Method, r.Path)
	}
}
