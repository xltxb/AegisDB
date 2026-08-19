# 13 · 异步导出 5,000,000 行上限写死 + 超限失败遗留孤儿文件

Status: ready-for-human（已修复，待人工验收）

## 现象

异步导出大结果集时报"导出超过行数上限 5000000 行,请缩小查询范围或分批导出"。该上限（及 2GB 字节上限）是代码写死的：`export.go` 里 setter 注释写着 "tests/config"，但 config 接线从未存在，只有测试调用——运维想放宽只能改代码重新发版。

排查中另发现一个真 bug（B3）：导出任务中途失败（撞上限、或目标库中途报错）时，已经 flush 落盘的 part 文件（每个最多 100MB）被原样遗留——失败任务不记录 files，`ownsExportFile` 只认已记录文件，所以这些加密压缩包既不能下载也永远没人清理。一次 5GB 的失败导出就在磁盘上留 5GB 孤儿文件，与上限"防占满磁盘"的初衷相反。

## 修复

**backend/internal/service/export.go**
- 新增 `exportLimits()`：每个任务启动时从设置读 `export.maxRows` / `export.maxBytes`（0=不限，负值回退默认），内置默认 5,000,000 行 / 2,000,000,000 字节不变；`exportLimitErr` 改为显式传上限。报错文案追加"上限可在 系统设置·网关 调整"。
- 新增 `partWriter.discard()`；`produceExport` 所有失败路径统一走 `fail()`，删除已落盘的 part 文件（删除失败仅记日志）。
- 清理两处"there is no row limit"的过期注释。

**backend/internal/bootstrap/seed.go**：settings 种子补 `export.maxRows` / `export.maxBytes` 默认值（老库不回填也生效，settingInt 有同值缺省）。

**frontend**：SettingsView 导出区块新增两个数字输入（行数/字节上限，0=不限），zh/en 文案补齐；构建通过（vue-i18n 严格编译）。

## 回归测试

- `service/export_limit_test.go`：适配新签名，补 0=不限断言。
- `bootstrap/export_cap_config_test.go` · `TestExport_RowCapConfigurableAndFailureLeavesNoOrphans`：设置 maxRows=50 → 任务失败且报错引用配置值、导出目录 0 个遗留文件；改 maxRows=0 → 同一导出成功。

后端全量测试 + 前端 build 均通过。
