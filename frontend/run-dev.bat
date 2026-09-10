@echo off
REM 启动前端开发服务器（Vite，代理 /api 到后端 :8080）
REM 双击运行，或在 frontend 目录执行：run-dev.bat
setlocal
if not exist node_modules (
  echo [AegisDB] 首次运行，安装依赖...
  call npm install
)
echo [AegisDB] 前端 -^> http://localhost:5173
call npm run dev
endlocal
