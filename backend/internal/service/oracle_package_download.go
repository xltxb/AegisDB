package service

// Oracle 程序包下载:把包头 + 包体拼成 .pck 文件交给浏览器。
//
// .pck 是 PL/SQL Developer 的约定:CREATE OR REPLACE PACKAGE … 与 CREATE OR REPLACE
// PACKAGE BODY … 两段,各自以单独一行的 "/" 收尾。按这个样子给,文件拿到 SQL*Plus /
// SQLcl 里 @ 一下、或在 PL/SQL Developer 里打开执行,就能原样重建这个包,不用手工补斜杠。
//
// 读的是与「查看源码」同一份东西、同一道访问控制(objectSourceText)。多出来的一步是
// 审计:查看停在屏幕上,下载是源码变成文件离开网关 —— 这正是 ResultExported 记的那类事。
//
// 选一个包给 .pck,选多个打成一个 zip:浏览器会拦截连续弹出的多个下载,一个文件才靠得住。

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// maxPackageDownload 一次最多下载多少个包。一个 schema 几百个包是常事,但一次把几千个
// 包源码拖走不是"下载几个包"这个动作该有的样子;上限也挡住一个请求把目标库的
// ALL_SOURCE 翻个底朝天。
const maxPackageDownload = 200

// ErrNoPackages:请求里没有一个有效的包名。
var ErrNoPackages = errors.New("请选择要下载的程序包")

// PackageDownload 是交给 handler 的文件。
type PackageDownload struct {
	Filename    string
	ContentType string
	Body        []byte
}

// DownloadPackages 取 scope(Oracle 的 owner)下若干个程序包的源码,打成 .pck / zip。
// 任何一个包取不到就整体失败并说出是哪一个:少了几个包的 zip 比报错更糟 —— 拿到的人
// 会以为那就是全部。
func (s *Services) DownloadPackages(u *model.User, connID int64, scope, database string, names []string) (*PackageDownload, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	applyTargetDatabase(conn, database)
	if !gateway.IsOracleEngine(conn.Engine) {
		return nil, apimsg.Errorf("程序包是 Oracle 专有对象,当前实例 %s 的引擎为 %s", conn.Name, conn.Engine)
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil, apimsg.Errorf("缺少 scope(包所在的 schema)")
	}
	pkgs := uniqueNames(names)
	if len(pkgs) == 0 {
		return nil, ErrNoPackages
	}
	if len(pkgs) > maxPackageDownload {
		return nil, apimsg.Errorf("一次最多下载 %d 个程序包,当前选了 %d 个", maxPackageDownload, len(pkgs))
	}

	files := make([]pckFile, 0, len(pkgs))
	for _, name := range pkgs {
		src, err := objectSourceText(conn, scope, "package", name)
		if err != nil {
			return nil, apimsg.Errorf("程序包 %s: %w", name, err)
		}
		files = append(files, pckFile{name: name, body: FormatPck(src)})
	}

	var out *PackageDownload
	if len(files) == 1 {
		out = &PackageDownload{
			Filename:    pckFilename(files[0].name),
			ContentType: "text/plain; charset=utf-8",
			Body:        []byte(files[0].body),
		}
	} else {
		body, err := zipPck(files)
		if err != nil {
			return nil, err
		}
		out = &PackageDownload{
			Filename:    fmt.Sprintf("%s_packages_%s.zip", safeFileStem(scope), time.Now().Format("20060102_150405")),
			ContentType: "application/zip",
			Body:        body,
		}
	}

	// 审计记在这个实例上,形状与终端会话导出一致:开头的 \pck 标记让它不会被当成 SQL。
	label := fmt.Sprintf(`\pck %s · %d 个程序包:%s`, out.Filename, len(pkgs),
		clip(scope+"."+strings.Join(pkgs, ", "+scope+"."), 900))
	s.recordAudit(u, conn, label, model.RiskLow, model.ResultExported, "", "")
	return out, nil
}

type pckFile struct {
	name string
	body string
}

// FormatPck 把一段 Oracle 源码整理成可直接执行的 .pck 文本:统一换行为 LF,
// 去掉尾部空白,保证最后一个单元也以单独一行的 "/" 收尾。
//
// 不加 UTF-8 BOM:SQL*Plus 会把 BOM 当成第一条命令的一部分,报 SP2-0734。
func FormatPck(src string) string {
	s := strings.ReplaceAll(src, "\r\n", "\n")
	s = strings.TrimRight(s, " \t\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if strings.TrimSpace(lines[len(lines)-1]) != "/" {
		s += "\n/"
	}
	return s + "\n"
}

func zipPck(files []pckFile) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := map[string]int{}
	for _, f := range files {
		fn := pckFilename(f.name)
		// 两个包名只差在被替换掉的字符上时,落到同一个文件名 —— 编号区分,不许后一个
		// 悄悄盖掉前一个。
		key := strings.ToLower(fn)
		if n := used[key]; n > 0 {
			fn = fmt.Sprintf("%s_%d.pck", strings.TrimSuffix(fn, ".pck"), n+1)
		}
		used[key]++
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fn, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pckFilename(name string) string { return safeFileStem(name) + ".pck" }

// safeFileStem 把对象名变成安全的文件名:Oracle 带引号的名字几乎什么字符都能有,
// 路径分隔符、Windows 保留字符与控制字符一律换成下划线,zip 里也就不会出现 ../ 之类的路径。
func safeFileStem(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	stem := strings.Trim(b.String(), ". ")
	if stem == "" {
		return "package"
	}
	return stem
}

// uniqueNames 去空白、去空、去重,保持原顺序。
func uniqueNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
