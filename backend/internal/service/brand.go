package service

// 系统名称与 LOGO(品牌)可配置(.scratch/branding)。
//
// 三个键都存在 tbl_setting 里:多活各节点读的是同一张表,不靠本机磁盘。LOGO 以 data URI 原样入库
// (上限 BrandLogoMaxBytes),浏览器只拿它当 <img> / favicon 用。
//
// **没配置时各处保持原来的字样**:界面与 TOTP / Webhook / 接口文档写的是 "DP DB GATEWAY",
// SQL 导出抬头、飞书卡片写的是 "Vela 数据库网关"。所以默认值不在这里统一,由每个调用点自己带
// (Brand.Text 的 def 参数),配置了才统一换成配置的名称。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"velagateway/internal/apimsg"
	"velagateway/internal/repository"
)

const (
	SettingBrandName   = "brand.name"   // 中文名称(中文界面优先用它)
	SettingBrandNameEn = "brand.nameEn" // 英文名称(英文界面优先用它)
	SettingBrandLogo   = "brand.logo"   // data:image/...;base64,...;空 = 用默认图标

	// BrandNameMaxRunes 名称长度上限:侧栏、登录页、浏览器标签页都要放得下。
	BrandNameMaxRunes = 40
	// BrandLogoMaxBytes LOGO 解码后的字节上限。它跟着每次打开页面下发,不能太大。
	BrandLogoMaxBytes = 256 << 10

	// 未配置时各调用点的原字样。
	BrandDefaultUI     = "DP DB GATEWAY"
	BrandDefaultLegacy = "Vela 数据库网关"
)

// Brand 是当前生效的品牌配置。空字段 = 未配置。
type Brand struct {
	Name   string `json:"name"`
	NameEn string `json:"nameEn"`
	Logo   string `json:"logo"`
}

// Text 返回某处该显示的名称:en 时 英文名 → 中文名 → def;否则 中文名 → 英文名 → def。
// 只要配了任意一个名称,就不再回落到 def —— 改了名却在某处还露出旧名,比不改更糟。
func (b Brand) Text(def string, en bool) string {
	first, second := b.Name, b.NameEn
	if en {
		first, second = b.NameEn, b.Name
	}
	switch {
	case first != "":
		return first
	case second != "":
		return second
	}
	return def
}

// LoadBrand 读当前品牌配置。repo 为 nil(单元测试里的裸 Services)时返回空配置。
func LoadBrand(repo *repository.Repo) Brand {
	if repo == nil {
		return Brand{}
	}
	get := func(k string) string {
		v, err := repo.GetSetting(k)
		if err != nil || v == "" {
			return ""
		}
		var s string
		if json.Unmarshal([]byte(v), &s) != nil {
			return ""
		}
		return strings.TrimSpace(s)
	}
	return Brand{Name: get(SettingBrandName), NameEn: get(SettingBrandNameEn), Logo: get(SettingBrandLogo)}
}

// Brand 读当前品牌配置。
func (s *Services) Brand() Brand {
	if s == nil {
		return Brand{}
	}
	return LoadBrand(s.Repo)
}

// NormalizeBrandName 校验并规整一个名称:去首尾空白;空串合法(= 未配置)。
func NormalizeBrandName(v string) (string, error) {
	s := strings.TrimSpace(v)
	if utf8.RuneCountInString(s) > BrandNameMaxRunes {
		return "", apimsg.Errorf("系统名称不能超过 %d 个字", BrandNameMaxRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return "", errors.New("系统名称不能包含换行、控制字符或尖括号")
		}
	}
	return s, nil
}

// brandLogoTypes:允许的图片类型 → 文件头魔数(SVG 是文本,单独检查)。
var brandLogoTypes = map[string][]byte{
	"image/png":                {0x89, 'P', 'N', 'G'},
	"image/jpeg":               {0xFF, 0xD8, 0xFF},
	"image/gif":                []byte("GIF8"),
	"image/webp":               []byte("RIFF"),
	"image/x-icon":             {0, 0, 1, 0},
	"image/vnd.microsoft.icon": {0, 0, 1, 0},
	"image/svg+xml":            nil,
}

// NormalizeBrandLogo 校验 LOGO:空串合法(= 用默认图标);否则必须是允许类型的 base64 data URI,
// 解码后不超过 BrandLogoMaxBytes,且文件头与声明的类型一致。SVG 不许带脚本或事件属性 ——
// 页面上只当 <img> 用,本来就不会执行,这里再挡一道,免得有人把它另存出去直接打开。
func NormalizeBrandLogo(v string) (string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "", nil
	}
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return "", errors.New("LOGO 必须是上传的图片")
	}
	mime, payload, ok := strings.Cut(rest, ";base64,")
	if !ok {
		return "", errors.New("LOGO 必须是上传的图片")
	}
	mime = strings.ToLower(mime)
	magic, known := brandLogoTypes[mime]
	if !known {
		return "", errors.New("LOGO 只支持 PNG、JPG、GIF、WebP、ICO、SVG 图片")
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > BrandLogoMaxBytes+3 {
		return "", apimsg.Errorf("LOGO 图片不能超过 %d KB", BrandLogoMaxBytes>>10)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("LOGO 图片数据已损坏")
	}
	if len(raw) == 0 {
		return "", errors.New("LOGO 图片数据已损坏")
	}
	if len(raw) > BrandLogoMaxBytes {
		return "", apimsg.Errorf("LOGO 图片不能超过 %d KB", BrandLogoMaxBytes>>10)
	}
	if mime == "image/svg+xml" {
		low := strings.ToLower(string(raw))
		if !strings.Contains(low, "<svg") {
			return "", errors.New("LOGO 图片内容与类型不符")
		}
		if strings.Contains(low, "<script") || strings.Contains(low, "javascript:") || strings.Contains(low, "<foreignobject") || svgEventAttr(low) {
			return "", errors.New("SVG 格式的 LOGO 不能包含脚本")
		}
	} else if !bytes.HasPrefix(raw, magic) || (mime == "image/webp" && (len(raw) < 12 || string(raw[8:12]) != "WEBP")) {
		return "", errors.New("LOGO 图片内容与类型不符")
	}
	return "data:" + mime + ";base64," + payload, nil
}

// svgEventAttr:有没有 onload= 这类事件属性(空白之后紧跟 on 字母…=)。
func svgEventAttr(low string) bool {
	for i := 0; i+3 < len(low); i++ {
		c := low[i]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '/' {
			continue
		}
		if low[i+1] != 'o' || low[i+2] != 'n' {
			continue
		}
		j := i + 3
		for j < len(low) && low[j] >= 'a' && low[j] <= 'z' {
			j++
		}
		if j == i+3 {
			continue
		}
		for j < len(low) && (low[j] == ' ' || low[j] == '\t' || low[j] == '\n' || low[j] == '\r') {
			j++
		}
		if j < len(low) && low[j] == '=' {
			return true
		}
	}
	return false
}
