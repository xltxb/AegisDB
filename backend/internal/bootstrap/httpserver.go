package bootstrap

import (
	"net/http"
	"time"
)

// NewHTTPServer 是网关对外服务用的 http.Server(2026-10 安全审计 H3)。
//
// 从前用 gin 的 r.Run,底下是一个什么超时都没有的 http.Server:一个连接慢慢地、一个字节一个
// 字节地发请求头,就能一直占着。这里:
//   - ReadHeaderTimeout:读完请求头的时限,挡的就是这种慢速攻击;
//   - IdleTimeout:keep-alive 连接空闲多久断开;
//   - MaxHeaderBytes:请求头大小上限。
//
// 刻意不设 ReadTimeout / WriteTimeout:它们按整个请求计时,脚本执行、导出下载、终端
// WebSocket 都会合法地跑上很久,设了就会被拦腰掐断。请求体大小由 middleware.BodyLimit 兜底。
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
