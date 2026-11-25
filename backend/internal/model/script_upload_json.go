package model

import (
	"encoding/json"
	"path/filepath"
)

// MarshalJSON 让 ScriptUpload 出现在任何响应里时,path 只是**相对用户目录的文件名**,
// 不是服务器绝对路径(2026-10 黑盒测试:/scripts/upload 回显了部署目录结构)。
// 库里存的 Path 不变,网关内部读文件、核对哈希照旧用它;对外只有这一个出口。
func (s ScriptUpload) MarshalJSON() ([]byte, error) {
	type plain ScriptUpload // 去掉方法集,免得递归
	p := plain(s)
	if p.Path != "" {
		p.Path = filepath.Base(p.Path)
	}
	return json.Marshal(p)
}
