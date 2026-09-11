@echo off
REM 被取代的 Vue 实现,留作对照与回退。默认 http://localhost:5174
REM 当前的前端是 React 版,在仓库根目录的 frontend\ 下。
cd /d %~dp0
if not exist node_modules (
  echo [AegisDB · Vue 旧版] 首次运行,安装依赖...
  call npm install
)
echo [AegisDB · Vue 旧版] -^> http://localhost:5174
call npm run dev
