package handler

// Swagger UI, served by the gateway itself at /docs.
//
// The assets are VENDORED and embedded in the binary (internal/handler/swaggerui),
// not pulled from a CDN. Swagger UI's own examples load from unpkg, and every one
// of those pages is a blank screen on a machine with no route to the internet —
// which is where a database gateway usually lives. Embedding also means the page
// cannot drift from the binary that serves it.
//
// They are stored GZIPPED (~425KB for both, against 1.6MB raw): that is what
// goes into the repository, into the binary, and — for any browser made this
// decade — onto the wire untouched.
//
// swagger-ui-dist is Apache-2.0; its LICENSE and NOTICE sit beside the assets.

import (
	"bytes"
	"compress/gzip"
	"embed"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// SpecPath is where the contract lives relative to the process's working
// directory — the same path the raw /openapi.yaml route serves, and the layout
// build.sh ships (binary + docs/ + configs/, with WorkingDirectory set to it).
const SpecPath = "docs/openapi.yaml"

//go:embed swaggerui/swagger-ui-bundle.js.gz swaggerui/swagger-ui.css.gz
var swaggerAssets embed.FS

// swaggerAsset maps a request path to the embedded file and its content type.
// Only these two are served: an allowlist, so the route can never be walked into
// the rest of the embedded filesystem.
var swaggerAsset = map[string]struct{ file, mime string }{
	"swagger-ui-bundle.js": {"swaggerui/swagger-ui-bundle.js.gz", "application/javascript; charset=utf-8"},
	"swagger-ui.css":       {"swaggerui/swagger-ui.css.gz", "text/css; charset=utf-8"},
}

// APIDocs serves the Swagger UI shell. The spec itself is fetched by the page
// from /openapi.yaml, so the contract has exactly one source and editing the
// YAML is visible on the next refresh.
func (h *Handler) APIDocs(c *gin.Context) {
	// A missing spec would leave Swagger UI showing its own generic failure. Say
	// what is actually wrong — the usual cause is a process started from the
	// wrong working directory.
	if _, err := os.Stat(SpecPath); err != nil {
		c.String(http.StatusNotFound,
			"接口契约未找到: %s(相对于进程工作目录)。开发时请在 backend/ 目录下启动,部署时确认 WorkingDirectory 指向安装目录。",
			SpecPath)
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	// The page carries no data of its own, but it is regenerated per release;
	// no-cache keeps a stale shell from pointing at assets a new build renamed.
	c.Header("Cache-Control", "no-cache")
	if err := swaggerShell.Execute(c.Writer, nil); err != nil {
		c.String(http.StatusInternalServerError, "渲染失败: %v", err)
	}
}

// APIDocsAsset serves one embedded Swagger UI file.
func (h *Handler) APIDocsAsset(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("file"), "/")
	a, ok := swaggerAsset[name]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	blob, err := swaggerAssets.ReadFile(a.file)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", a.mime)
	// Vendored assets change only when the dependency is bumped, and the bump
	// changes the binary — so a long cache is safe and saves 400KB per visit.
	c.Header("Cache-Control", "public, max-age=604800")
	if strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")
		_, _ = c.Writer.Write(blob)
		return
	}
	// No gzip support claimed: decompress rather than serve bytes the client
	// cannot read. Rare enough that doing it per request costs nothing.
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	defer zr.Close()
	_, _ = io.Copy(c.Writer, zr)
}

// swaggerShell is the page Swagger UI mounts into. Everything it loads is
// same-origin: the two embedded assets and /openapi.yaml.
var swaggerShell = template.Must(template.New("swagger").Parse(`<!doctype html>
<html lang="zh">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>AegisDB · 接口文档</title>
  <link rel="stylesheet" href="/docs/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafbfd; }
    /* Swagger UI's own topbar is the spec-URL explorer, which is noise when the
       page serves exactly one spec. */
    .swagger-ui .topbar { display: none; }
    .swagger-ui .info { margin: 26px 0 18px; }
    .swagger-ui .info .title { font-family: "Space Grotesk", "Noto Sans SC", -apple-system, sans-serif; }
    .vela-note {
      max-width: 1460px; margin: 18px auto 0; padding: 12px 18px;
      border-radius: 10px; background: #eef3ff; color: #1c41b8;
      font: 500 13px "Manrope", "Noto Sans SC", -apple-system, sans-serif; line-height: 1.7;
    }
    .vela-note code { font-family: "JetBrains Mono", ui-monospace, monospace; font-size: 12px;
      background: rgba(255,255,255,.7); padding: 1px 6px; border-radius: 5px; }
    .vela-note a { color: #1c41b8; }
  </style>
</head>
<body>
  <div class="vela-note">
    先点右上角 <strong>Authorize</strong> 填凭据再 <strong>Try it out</strong>:控制台接口用
    <code>bearerAuth</code>(登录拿到的 JWT),外部系统的开放接口用 <code>openApiKey</code>
    (<code>&lt;key&gt;.&lt;secret&gt;</code>)。原始契约:<a href="/openapi.yaml">/openapi.yaml</a>。
    <strong>Try it out 打的是真接口</strong> —— 对生产实例执行前想清楚。
  </div>
  <div id="swagger-ui"></div>
  <script src="/docs/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '/openapi.yaml',
      dom_id: '#swagger-ui',
      // deep links are what make /docs#/pipelines/post_releases a shareable
      // address — the way the reference this was modelled on behaves.
      deepLinking: true,
      docExpansion: 'list',
      defaultModelsExpandDepth: 1,
      defaultModelRendering: 'model',
      displayRequestDuration: true,
      filter: true,
      tryItOutEnabled: true,
      // Keep the token across refreshes: the alternative is re-pasting a JWT
      // every time the page reloads, which is how people end up not using auth.
      persistAuthorization: true,
      presets: [SwaggerUIBundle.presets.apis],
      layout: 'BaseLayout'
    });
  </script>
</body>
</html>
`))
