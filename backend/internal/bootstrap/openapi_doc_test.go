package bootstrap

// The API document is only useful if it is COMPLETE, and a document nothing
// checks drifts within a release: this suite found docs/openapi.yaml covering 55
// of 110 routes. Route tables change every sprint; nobody notices a missing
// paragraph. So the check is mechanical — add a route without documenting it and
// this test says which one.
//
// It is deliberately a static parse of router.go rather than a walk of gin's
// tree: the guards (menu / admin / scope) live in the source line, and a reader
// of the document needs to know them. Registering a route is the moment to write
// down what it costs to call.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	// a.GET("/x", …) / v1.POST(…) / open.GET(…) / r.GET(…)
	routeRe = regexp.MustCompile(`\b(a|v1|open|r)\.(GET|POST|PUT|PATCH|DELETE)\(\s*"([^"]*)"`)
	// a top-level path key in the YAML: two spaces, a leading slash, then a colon
	docPathRe   = regexp.MustCompile(`^  (/\S*):\s*$`)
	docMethodRe = regexp.MustCompile(`^    (get|post|put|patch|delete):`)
	ginParamRe  = regexp.MustCompile(`:([A-Za-z][A-Za-z0-9]*)`)
)

// notAnAPIRoute is the exception list, and it is deliberately spelled out route
// by route rather than as a pattern: an exclusion that matches a shape is how a
// guard quietly stops guarding. Everything here serves the DOCUMENTATION UI
// itself — writing its JS bundle into the API contract would hand integrators a
// path that answers with a script.
var notAnAPIRoute = map[string]bool{
	"GET /docs/*file": true, // Swagger UI's own assets (see handler.APIDocsAsset)
}

// routesInCode returns every registered route as "METHOD /path", with gin's
// :param rewritten to OpenAPI's {param} and the open-API group's prefix applied.
func routesInCode(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	out := map[string]bool{}
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		group, method, path := m[1], m[2], m[3]
		if path == "" {
			continue // the group declaration itself, e.g. v1.Group("")
		}
		if group == "open" {
			path = "/open" + path
		}
		route := method + " " + ginParamRe.ReplaceAllString(path, "{$1}")
		if notAnAPIRoute[route] {
			continue
		}
		out[route] = true
	}
	if len(out) < 50 {
		t.Fatalf("route parser found only %d routes — the parser is broken, not the router", len(out))
	}
	return out
}

// pathsInDoc returns every documented "METHOD /path".
func pathsInDoc(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	out := map[string]bool{}
	inPaths, cur := false, ""
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "paths:") {
			inPaths = true
			continue
		}
		if !inPaths {
			continue
		}
		if m := docPathRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			continue
		}
		if m := docMethodRe.FindStringSubmatch(line); m != nil && cur != "" {
			out[strings.ToUpper(m[1])+" "+cur] = true
		}
	}
	if len(out) < 50 {
		t.Fatalf("doc parser found only %d operations — the parser is broken, not the document", len(out))
	}
	return out
}

// TestOpenAPICoversEveryRoute — every route the server answers is written down.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	code, doc := routesInCode(t), pathsInDoc(t)
	var missing []string
	for r := range code {
		if !doc[r] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	for _, r := range missing {
		t.Errorf("route %s is not in docs/openapi.yaml", r)
	}
	if len(missing) > 0 {
		t.Logf("%d of %d routes undocumented", len(missing), len(code))
	}
}

// TestOpenAPIDocumentsNoGhostRoutes — and nothing is written down that the
// server does not answer. A stale entry is worse than a missing one: an
// integrator writes code against it and gets a 404 with no clue why.
func TestOpenAPIDocumentsNoGhostRoutes(t *testing.T) {
	code, doc := routesInCode(t), pathsInDoc(t)
	// Two routes are served from the ROOT, outside the /api/v1 server prefix;
	// both carry their own `servers:` override in the document.
	code["GET /healthz"] = true
	code["GET /docs"] = true
	var ghosts []string
	for d := range doc {
		if !code[d] {
			ghosts = append(ghosts, d)
		}
	}
	sort.Strings(ghosts)
	for _, d := range ghosts {
		t.Errorf("docs/openapi.yaml documents %s, which no route serves", d)
	}
}

// TestOpenAPIDeclaresGuards — the document states what a call COSTS. Every
// guarded route carries its guard in the summary as "[menu · admin]" or
// "[scope …]", because "can I call this" is the first question a reader has and
// the answer is not derivable from the path.
func TestOpenAPIDeclaresGuards(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	b, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	docText := string(b)

	// Pull the guard off each route's registration line.
	lineRe := regexp.MustCompile(`(?m)^.*\b(a|open)\.(GET|POST|PUT|PATCH|DELETE)\(\s*"([^"]*)".*$`)
	menuRe := regexp.MustCompile(`menu\("([a-z]+)"\)`)
	scopeRe := regexp.MustCompile(`RequireScope\(model\.Scope(\w+)\)`)
	// scope constant name → the scope string the summary should quote
	scopeText := map[string]string{
		"ReleaseCreate": "release:create", "ReleaseRead": "release:read", "ReviewCheck": "review:check",
	}

	for _, m := range lineRe.FindAllStringSubmatch(string(src), -1) {
		line, group, path := m[0], m[1], m[3]
		if path == "" {
			continue
		}
		var want string
		if mm := menuRe.FindStringSubmatch(line); mm != nil {
			want = mm[1]
			if strings.Contains(line, ", admin,") {
				want += " · admin"
			}
		} else if sm := scopeRe.FindStringSubmatch(line); sm != nil {
			want = "scope " + scopeText[sm[1]]
		}
		if want == "" {
			continue // open to any authenticated caller — nothing to declare
		}
		if group == "open" {
			path = "/open" + path
		}
		docPath := ginParamRe.ReplaceAllString(path, "{$1}")
		// The guard has to appear somewhere in the document. A per-operation
		// check would need a YAML parser; this catches the case that actually
		// happens — a guard nobody wrote down at all.
		if !strings.Contains(docText, "["+want+"]") {
			t.Errorf("route %s is guarded by [%s], which appears nowhere in docs/openapi.yaml", docPath, want)
		}
	}
}
