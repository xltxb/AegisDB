# 19 · 终端脚本执行报 "Data too long for column 'command'"

Status: ready-for-human（已修复；升级需先应用迁移 0018）

## 现象

Web 命令行执行(粘贴)脚本时报:`脚本执行失败: 提交失败:Error 1406 (22001): Data too long for column 'command' at row 1`。

## 根因(两处叠加)

1. **粘贴脚本没走引用模型**:`SaveUploadedScript` 内部早已创建 ScriptUpload 记录,却只
   返回路径、把 id 丢了 → `SubmitScriptForApproval` 收到 uploadID=0,按遗留分支把
   **脚本全文**塞进审批单 `command`(TEXT 64KB)→ 1406。上传选择器路径早已改引用,
   粘贴路径漏网。
2. **摘要按行截断、不按字节**:`scriptExcerpt` 只截行数(40 行),单行 80KB 的生成语句
   (超长 IN 列表)整行进工单——摘要本身失去了"有界"这个存在意义。

## 修复

- ScriptExecute 粘贴分支贯穿 upload id:粘贴即上传,工单存**有界摘要+文件引用+sha256**,
  批准后按引用模型重读文件、逐条重判、逐条执行(既有机制,无新面)。
- `scriptExcerpt` 每行钳制 300 字节(按 rune 边界截断,行尾标注"本行截断")。
- 终端 Exec 与遗留 uploadID=0 分支补 15MB 存储上限守卫(`ErrSQLTooLong`),REST/WS 两端
  透传可行动报错,不再折叠成"连接不存在"。

## 回归测试

- `script_paste_ref_test.go`:>64KB 粘贴脚本(含单行 80KB 语句)→ 拦截转审批,工单
  command 为有界摘要(<8KB)且带 scriptUploadId 引用。修复前红(80,201 字节全文)。
- 后端全量通过。

## 升级注意

与 issue 18 同批:**需先跑 `./vela-gateway migrate`(迁移 0018)**。用户报错时若尚未
升级 8171cb1+,请一并升级;粘贴脚本路径修复后,即使不放宽列宽,工单也只存摘要。
