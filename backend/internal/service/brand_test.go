package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

func TestBrandText_FallbackOrder(t *testing.T) {
	cases := []struct {
		b    Brand
		en   bool
		want string
	}{
		{Brand{}, false, "DEF"},
		{Brand{}, true, "DEF"},
		{Brand{Name: "中文"}, false, "中文"},
		{Brand{Name: "中文"}, true, "中文"},
		{Brand{NameEn: "EN"}, false, "EN"},
		{Brand{NameEn: "EN"}, true, "EN"},
		{Brand{Name: "中文", NameEn: "EN"}, false, "中文"},
		{Brand{Name: "中文", NameEn: "EN"}, true, "EN"},
	}
	for _, c := range cases {
		if got := c.b.Text("DEF", c.en); got != c.want {
			t.Errorf("%+v en=%v: got %q want %q", c.b, c.en, got, c.want)
		}
	}
}

func TestBrand_NilServicesIsUnconfigured(t *testing.T) {
	var s *Services
	if b := s.Brand(); b != (Brand{}) {
		t.Errorf("nil Services 应视作未配置,got %+v", b)
	}
	if b := (&Services{}).Brand(); b != (Brand{}) {
		t.Errorf("无 Repo 应视作未配置,got %+v", b)
	}
}

func TestNormalizeBrandLogo(t *testing.T) {
	enc := func(mime string, raw []byte) string {
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	}
	good := []string{
		"",
		enc("image/png", []byte{0x89, 'P', 'N', 'G', 1, 2}),
		enc("image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}),
		enc("image/gif", []byte("GIF89a")),
		enc("image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")),
		enc("image/x-icon", []byte{0, 0, 1, 0, 1}),
		enc("image/svg+xml", []byte(`<svg viewBox="0 0 1 1" font="x"><g transform="none"/></svg>`)),
		enc("IMAGE/PNG", []byte{0x89, 'P', 'N', 'G'}),
	}
	for _, g := range good {
		if _, err := NormalizeBrandLogo(g); err != nil {
			t.Errorf("应接受 %.60s:%v", g, err)
		}
	}
	bad := []string{
		"logo.png",
		"data:image/png,abc",
		enc("image/bmp", []byte("BM")),
		enc("image/webp", []byte("RIFF\x00\x00\x00\x00AVI ")),
		enc("image/png", nil),
		enc("image/svg+xml", []byte(`<html></html>`)),
		enc("image/svg+xml", []byte(`<svg><a href="javascript:alert(1)"/></svg>`)),
		enc("image/svg+xml", []byte(`<svg><foreignObject/></svg>`)),
		enc("image/svg+xml", []byte("<svg\nonclick = \"x\"></svg>")),
		enc("image/png", append([]byte{0x89, 'P', 'N', 'G'}, make([]byte, BrandLogoMaxBytes)...)),
	}
	for _, b := range bad {
		if _, err := NormalizeBrandLogo(b); err == nil {
			t.Errorf("应拒绝 %.60s", b)
		}
	}
	if got, _ := NormalizeBrandLogo(enc("IMAGE/PNG", []byte{0x89, 'P', 'N', 'G'})); !strings.HasPrefix(got, "data:image/png;") {
		t.Errorf("类型应规整成小写:%s", got)
	}
}

func TestNormalizeBrandName(t *testing.T) {
	if got, err := NormalizeBrandName("  星河 网关 "); err != nil || got != "星河 网关" {
		t.Errorf("去首尾空白:%q %v", got, err)
	}
	if _, err := NormalizeBrandName(strings.Repeat("字", BrandNameMaxRunes)); err != nil {
		t.Errorf("%d 个字应接受:%v", BrandNameMaxRunes, err)
	}
	for _, bad := range []string{strings.Repeat("字", BrandNameMaxRunes+1), "a\tb", "a<b", "x\u0000"} {
		if _, err := NormalizeBrandName(bad); err == nil {
			t.Errorf("应拒绝 %q", bad)
		}
	}
}

// 没配置时,后端拼出的品牌字样与改动前一字不差。
func TestBrand_DefaultsKeepLegacyText(t *testing.T) {
	s := &Services{}
	pre := s.sqlExportPreamble(&model.Connection{Engine: "MySQL 8.0"}, &model.ExportJob{}, "SELECT 1", true, time.Now())
	if !strings.HasPrefix(pre, "-- AegisDB 数据库网关 · 数据导出(SQL)\n") {
		t.Errorf("SQL 导出抬头变了:%q", pre[:60])
	}
	ev := buildPlatformEvent("audit", map[string]any{}, time.Now(), LoadBrand(nil).Text(BrandDefaultUI, true))
	if ev["source_system"] != "AegisDB" {
		t.Errorf("Webhook source_system 变了:%v", ev["source_system"])
	}
	card := larkApprovalCard(&model.Approval{ApNo: "AP-1"}, "", LoadBrand(nil).Text(BrandDefaultLegacy, false))
	if !strings.Contains(toJSONForTest(card), "AegisDB 数据库网关 · AP-1") {
		t.Error("飞书卡片落款变了")
	}
	card = larkApprovalCard(&model.Approval{ApNo: "AP-1"}, "", Brand{Name: "星河"}.Text(BrandDefaultLegacy, false))
	if !strings.Contains(toJSONForTest(card), "星河 · AP-1") {
		t.Error("飞书卡片落款应换成配置的名称")
	}
}

func toJSONForTest(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
