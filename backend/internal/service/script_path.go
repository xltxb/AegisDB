package service

import (
	"path/filepath"
	"strings"
)

// 落库的脚本路径用 `/`,读的时候转回本机分隔符。
//
// `filepath.Join` 用的是**当前这台机器**的分隔符。在 Windows 上开发时落库的是
//
//	uploads\Lin_Wei\20260908-155637.379_dup-check.sql
//
// 这一行在 Linux 或 macOS 上读不了 —— 反斜杠不是路径分隔符,整串被当成**一个文件名**,
// os.Stat 找不到,接口回「脚本文件不可用或不属于当前用户」。文件明明就在那儿。
//
// 库里存的东西要能跨机器读,所以它不该带任何一台机器的习惯。

// storedScriptPath 把一个本机路径变成可以落库的形式。
func storedScriptPath(p string) string { return filepath.ToSlash(p) }

// localScriptPath 把库里存的路径变回**这台机器**的形式。
//
// 两种分隔符都认:存量那些在 Windows 上落的反斜杠路径不会自己变好,而它们指向的文件
// 就在那里 —— 认回来是一行代码的事,让人重新上传一遍不是。
//
// 反斜杠在 Linux 上是合法的文件名字符,所以这个转换理论上会错认一个名字里真带反斜杠
// 的文件。而上传落库的名字都过了 sanitizeFilename,那里不放行反斜杠 —— 换句话说,库里
// 的路径里出现反斜杠只有一个来源:它是在 Windows 上写下的分隔符。
func localScriptPath(p string) string {
	return filepath.FromSlash(strings.ReplaceAll(p, `\`, "/"))
}
