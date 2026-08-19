package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// buildSPADir writes a minimal built-frontend layout: index.html plus one
// content-hashed bundle under assets/.
func buildSPADir(t *testing.T, withAssets bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><script src=/assets/app-NEW.js></script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withAssets {
		if err := os.Mkdir(filepath.Join(dir, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "assets", "app-NEW.js"), []byte("console.log(1)"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func spaGet(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// After a deploy, a browser holding last release's cached index.html asks for
// the OLD hashed bundle. gin's Static falls through to NoRoute on a missing
// file, and the fallback used to answer with index.html — which the browser
// then rejected with "Expected a JavaScript module but the server responded
// with a MIME type of text/html", pointing nowhere near the real cause. A
// missing file must be a 404; only extension-less client-side routes get the
// SPA fallback, and index.html itself must always revalidate.
func TestServeSPA_MissingAssetIs404NotIndexHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	serveSPA(r, buildSPADir(t, true))

	// stale hashed bundle → 404, never index.html-as-javascript
	if w := spaGet(t, r, "/assets/app-OLD.js"); w.Code != http.StatusNotFound {
		t.Errorf("missing asset: want 404, got %d (body %q)", w.Code, w.Body.String())
	}
	// any extension-bearing miss outside /assets too
	if w := spaGet(t, r, "/logo-OLD.png"); w.Code != http.StatusNotFound {
		t.Errorf("missing file: want 404, got %d", w.Code)
	}

	// the real bundle serves as javascript with a forever cache (content-hashed)
	w := spaGet(t, r, "/assets/app-NEW.js")
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Errorf("real asset: got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("hashed asset should be immutable-cacheable, got %q", cc)
	}

	// client-side routes still fall back to index.html, marked always-revalidate
	for _, p := range []string{"/", "/audit"} {
		w := spaGet(t, r, p)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "app-NEW.js") {
			t.Errorf("%s: SPA fallback broken: %d %q", p, w.Code, w.Body.String())
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("%s: index.html must revalidate, got Cache-Control %q", p, cc)
		}
	}
}

// A web dir deployed without its assets/ subdir must answer asset requests with
// 404 (surfacing the broken deploy), not serve index.html as the bundle.
func TestServeSPA_NoAssetsDirIs404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	serveSPA(r, buildSPADir(t, false))
	if w := spaGet(t, r, "/assets/app-NEW.js"); w.Code != http.StatusNotFound {
		t.Errorf("assets dir missing: want 404, got %d (body %q)", w.Code, w.Body.String())
	}
}
