# 14 · 异步导出报 unexpected EOF

Status: ready-for-human（已修复网关侧成因，待真实环境验证）

## 现象

异步导出（真实 MySQL 连接）任务失败，错误只有一句 "unexpected EOF"。这是 Go MySQL 驱动在 TCP 连接**中途被断开**时的原始报错，对操作者毫无信息量。

## 成因分析（两处在我们代码里，一类在目标库侧）

1. **MySQL DSN 写死 `ReadTimeout=60s`**（`realdb.go engineDriver`）。这是套接字级"单次读"超时，被该连接池的**所有**调用方共享：大导出排序阶段服务端超过 60 秒不吐首行、或异步通道里宣称支持 30–60min 的长语句静默超过 60 秒，驱动就在包中间掐断连接——上层报 unexpected EOF / invalid connection，与调用方自己的 30–90 分钟预算完全矛盾。每条真实执行路径本就带 QueryContext/ExecContext 的独立超时（驱动官方取消机制），套接字级再叠一个 60s 是纯粹的误伤。
2. **导出整体超时写死 30min 且超时报错不可辨认**（`RealQueryEach`）。行数上限放开后（issue 13）合法大导出可能超过 30 分钟；deadline 掐掉连接后，驱动可能把它报成传输错误而不是 context 超时，用户看到的还是 unexpected EOF。
3. **目标库/中间层主动断开**：net_write_timeout、wait_timeout、代理空闲超时、语句被 kill——不受网关控制，但报错必须给出方向。

## 修复

- `realdb.go`：删除 MySQL DSN 的 `ReadTimeout`，超时全部交给各调用路径已有的 context（拨号 8s 超时保留）。同时修好了异步执行通道 >60s 的 MySQL 语句必死的潜在 bug。
- `RealQueryEach`：整体超时改由调用方传入；deadline 触发后无论驱动报什么，都重新包裹 `context.DeadlineExceeded`，上层可 `errors.Is` 识别。
- `service/export.go`：新增 `export.execTimeout` 设置（秒，默认 1800）；新增 `exportExecErr` 翻译——超时报"导出执行超时(N分钟),已读取 X 行,可调大设置或分批"；unexpected EOF / invalid connection / connection reset / broken pipe 报"目标数据库中途断开(已读取 X 行)"并列出常见原因（net_write_timeout/wait_timeout/代理超时/语句被终止）；其他错误原样透传。
- seed 补 `export.execTimeout` 默认值；设置页导出区块新增"导出执行超时(秒)"输入框（zh/en 文案）。

## 回归测试

- `gateway/realdb_timeout_test.go`：MySQL DSN 断言不含 readTimeout；sqlite 递归 CTE + 50ms 超时验证 `errors.Is(err, DeadlineExceeded)` 成立。
- `service/export_execerr_test.go`：超时翻译（含行数与分钟数）、断连翻译（含行数与 net_write_timeout 提示）、普通错误透传。

后端全量测试 + 前端构建通过。

## 遗留

- 若真实环境的断开来自目标库侧参数（如 net_write_timeout=60），网关新报错会明说，但仍需 DBA 调大目标库参数或分批导出。
- part 落盘时 gzip+AES 加密 100MB 是同步的，会短暂停读（秒级）；如目标库 net_write_timeout 极小仍可能触发断开，可作为后续优化（流式压缩降低停顿）。
