# 17 · 部署后前端报 "Expected a JavaScript module … MIME type of text/html"

Status: ready-for-human（已修复，待线上验证）

## 现象

生产部署新包后浏览器控制台报:
`Failed to load module script: Expected a JavaScript-or-Wasm module script but the server responded with a MIME type of "text/html"`。

## 根因

`serveSPA` 的 NoRoute 回退对**一切**未匹配 GET 返回 index.html,而 gin 的 `r.Static`
在文件不存在时会落到 NoRoute。两类部署场景于是都变成同一个费解报错:

1. **旧 index.html 被浏览器/反代缓存**,请求上一版哈希名的 bundle(新包里已删)→ 服务端把
   index.html 当 JS 发回;
2. **web/assets 没部署到位**(解压不完整/web_dir 指错/启动早于解压)→ 同样全落回 index.html。

## 修复(backend/internal/bootstrap/router.go serveSPA)

- 资源类路径(/assets/* 或带扩展名)缺失 → **404**,只有无扩展名的前端路由才享受 SPA 回退;
- index.html 响应 `Cache-Control: no-cache`(必须每次协商——它携带当版哈希资源名);
- /assets/*(内容哈希命名)响应 `public, max-age=31536000, immutable`;
- 启动时 web_dir 缺 index.html 或 assets/ 直接 slog.Warn,坏部署当场可见。

## 回归测试(spa_serve_test.go)

- 缺失哈希 bundle → 404 而非 200 text/html;真实 bundle → javascript MIME + immutable;
- `/` 与 `/audit` 回退 index.html 且 no-cache;
- assets/ 目录整个缺失 → 404。

## 线上处置

1. 确认部署完整:`ls /opt/vela-gateway/web/assets | head`、
   `curl -I http://127.0.0.1:8080/assets/<任一js>` 应为 200 + javascript;
2. 换新包重启后,浏览器 Ctrl+F5 强刷(旧 index.html 缓存);若有反向代理,清其缓存;
3. 升级到本修复版后,index.html 不再被缓存住,后续发版不会复发。
