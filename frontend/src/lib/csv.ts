// CSV 单元格:需要时加引号;以 = + - @(或 Tab / CR)开头的单元格前加 ' 防公式注入 ——
// 表格软件会把它当公式执行(=HYPERLINK / =WEBSERVICE 能把数据带出去)。前导空白之后才出现的
// 公式头同样算:表格软件解析前会去掉空白(与后端 csvSanitize 同一条规则)。
export function csvCell(v: unknown): string {
  let s = String(v ?? '')
  if (/^[=+\-@\t\r]/.test(s) || /^\s+[=+\-@]/.test(s)) s = "'" + s
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}
