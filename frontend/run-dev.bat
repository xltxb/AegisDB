@echo off
REM 前端开发服务器(React 19 + Vite)。默认 http://localhost:5173
REM API 与终端 WebSocket 由 vite 代理转到本机 8080,所以要先起后端。
cd /d %~dp0
if not exist node_modules (
  echo [AegisDB] 首次运行,安装依赖...
  call npm install
)
echo [AegisDB] 前端 -^> http://localhost:5173
call npm run dev
