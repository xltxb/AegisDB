package handler

// The docs page is vendored third-party UI plus two of our own routes, so what
// is worth pinning is exactly the part we wrote: the shell points at the right
// spec, the embedded assets come back readable both gzipped and not, and a
// missing spec says WHY instead of letting Swagger UI show its generic failure.

import (
	"compress/gzip"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func docsRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{}
	r.GET("/docs", h.APIDocs)
	r.GET("/docs/*file", h.APIDocsAsset)
	return r
}

// atRepoRoot runs fn with the working directory at backend/, where SpecPath
// resolves — the handler reads the contract relative to the process's cwd.
func atRepoRoot(t *testing.T, fn func()) {
	t.Helper()
	wd, _ := os.Getwd()
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(wd)
	fn()
}

func TestAPIDocsShellPointsAtTheSpec(t *testing.T) {
	atRepoRoot(t, func() {
		w := httptest.NewRecorder()
		docsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/docs", nil))
		if w.Code != 200 {
			t.Fatalf("GET /docs = %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, want := range []string{
			"SwaggerUIBundle(",
			"url: '/openapi.yaml'", // one source for the contract
			"deepLinking: true",    // /docs#/tag/op stays a shareable address
			"persistAuthorization", // a token survives a refresh
			"/docs/swagger-ui-bundle.js",
			"/docs/swagger-ui.css",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the Swagger UI shell is missing %q", want)
			}
		}
		// Nothing may come from a CDN: this has to render with no route out.
		for _, bad := range []string{"unpkg.com", "cdn.jsdelivr.net", "cdnjs."} {
			if strings.Contains(body, bad) {
				t.Errorf("the shell loads %s — the page must be self-hosted", bad)
			}
		}
	})
}

func TestAPIDocsAssetsServeCompressedAndPlain(t *testing.T) {
	r := docsRouter()

	// Browsers claim gzip: the embedded bytes go out untouched.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/docs/swagger-ui-bundle.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("gzip request = %d", w.Code)
	}
	if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", enc)
	}
	zr, err := gzip.NewReader(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	plain, _ := io.ReadAll(zr)
	if !strings.Contains(string(plain), "SwaggerUIBundle") {
		t.Error("the decompressed bundle does not look like Swagger UI")
	}

	// A client that claims nothing gets readable bytes, not a gzip blob.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/docs/swagger-ui.css", nil))
	if w.Code != 200 {
		t.Fatalf("plain request = %d", w.Code)
	}
	if w.Header().Get("Content-Encoding") != "" {
		t.Error("served gzip to a client that did not ask for it")
	}
	if !strings.Contains(w.Body.String(), ".swagger-ui") {
		t.Error("the decompressed stylesheet does not look like Swagger UI's")
	}
}

// TestAPIDocsAssetAllowlist — the route takes a wildcard, so it must serve only
// the two files it knows, never a walk into the rest of the embedded tree.
func TestAPIDocsAssetAllowlist(t *testing.T) {
	r := docsRouter()
	for _, p := range []string{
		"/docs/LICENSE",
		"/docs/swaggerui/swagger-ui-bundle.js.gz",
		"/docs/../router.go",
		"/docs/nope.js",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code == 200 {
			t.Errorf("%s was served (%d bytes); only the two allowlisted assets may be", p, w.Body.Len())
		}
	}
}

// TestAPIDocsSaysWhyWhenSpecMissing — the failure people actually hit is a
// process started from the wrong directory.
func TestAPIDocsSaysWhyWhenSpecMissing(t *testing.T) {
	wd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(wd)

	w := httptest.NewRecorder()
	docsRouter().ServeHTTP(w, httptest.NewRequest("GET", "/docs", nil))
	if w.Code != 404 {
		t.Fatalf("missing spec = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), SpecPath) {
		t.Errorf("the error should name the path it looked for, got: %s", w.Body.String())
	}
}
