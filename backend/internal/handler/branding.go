package handler

// 系统名称与 LOGO(品牌)。存取走 /settings(管理员 + settings 菜单,改动进审计);这里只有
// 登录页要用的公开读接口、设置项校验,以及接口文档上的名称。见 service/brand.go。

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/internal/apimsg"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// Branding godoc
// @Summary 系统名称与 LOGO(公开)
// @Router  /auth/branding [get]
//
// 公开接口:未登录的登录页就要显示。只回这三项,不带任何别的设置。
func (h *Handler) Branding(c *gin.Context) {
	resp.OK(c, h.Svc.Brand())
}

func brandName(key string, v any) (any, error) {
	sv, err := str(key, v)
	if err != nil {
		return nil, err
	}
	return service.NormalizeBrandName(sv.(string))
}

func brandLogo(key string, v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, apimsg.Errorf("%s 必须是字符串", key)
	}
	return service.NormalizeBrandLogo(s)
}

// brandLogoAuditNote:审计里记 LOGO 的大小与摘要,不记 base64 本身。
func brandLogoAuditNote(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "已清除"
	}
	mime, payload, _ := strings.Cut(strings.TrimPrefix(s, "data:"), ";base64,")
	raw, _ := base64.StdEncoding.DecodeString(payload)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("已更新(%s,%d 字节,sha256:%s)", mime, len(raw), hex.EncodeToString(sum[:])[:16])
}

// specTitleLine 是 docs/openapi.yaml 里 info.title 那一行(文件里只出现一次;不带换行,CRLF / LF 都对得上)。配置了系统名称时,/openapi.yaml 把它换成
// 新名称;没配置时原样输出文件,一个字节不动。
const specTitleLine = "title: DP DB GATEWAY API"

// OpenAPISpec 输出接口契约。
func (h *Handler) OpenAPISpec(c *gin.Context) {
	b := h.Svc.Brand()
	if b.Name == "" && b.NameEn == "" {
		c.File(SpecPath)
		return
	}
	data, err := os.ReadFile(SpecPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	title := b.Text(service.BrandDefaultUI, false) + " API"
	// YAML 双引号字符串:名称里没有控制字符(NormalizeBrandName),只需转义 \ 和 "。
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(title) + `"`
	data = bytes.Replace(data, []byte(specTitleLine), []byte("title: "+quoted), 1)
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
}
