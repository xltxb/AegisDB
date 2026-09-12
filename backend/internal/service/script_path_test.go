package service

import (
	"path/filepath"
	"testing"
)

// 落库的脚本路径不能带宿主机的分隔符。
//
// `filepath.Join` 用的是**当前这台机器**的分隔符。在 Windows 上开发时,落库的是
//
//	uploads\Lin_Wei\20260908-155637.379_dup-check.sql
//
// 这一行在 Linux 或 macOS 上读不了 —— 反斜杠不是路径分隔符,整串被当成**一个文件名**,
// 于是 os.Stat 找不到,接口回「脚本文件不可用或不属于当前用户」。本仓库自带的那份
// vela-gateway.db 就是这个状态:四条记录的 path 全是反斜杠,而文件都好端端在那儿。
//
// 两头都要管:**写**的时候统一成 `/`(它在三个平台上都是合法分隔符),**读**的时候把
// 两种分隔符都认回来 —— 存量那些反斜杠路径不会自己变好。

func TestStoredScriptPath_UsesForwardSlashes(t *testing.T) {
	got := storedScriptPath(filepath.Join("uploads", "Lin_Wei", "a.sql"))
	if got != "uploads/Lin_Wei/a.sql" {
		t.Errorf("落库的路径是 %q —— 换个操作系统就读不了", got)
	}
}

func TestLocalScriptPath_ReadsBothSeparators(t *testing.T) {
	want := filepath.Join("uploads", "Lin_Wei", "a.sql")
	for _, stored := range []string{
		`uploads\Lin_Wei\a.sql`, // Windows 上落的库,存量就是这样
		`uploads/Lin_Wei/a.sql`, // 修好之后落的库
	} {
		if got := localScriptPath(stored); got != want {
			t.Errorf("localScriptPath(%q) = %q,期望 %q —— 存量记录不会自己变好", stored, got, want)
		}
	}
}

// 绝对路径同样要能用:部署时 uploads 常常配成 /var/lib/aegisdb/uploads。
func TestLocalScriptPath_KeepsAbsolutePaths(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "var", "lib", "aegisdb", "uploads", "a.sql")
	if got := localScriptPath(storedScriptPath(abs)); got != abs {
		t.Errorf("绝对路径走了一圈变成 %q,期望 %q", got, abs)
	}
}
