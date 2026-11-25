# 攻击面5 SSRF/密钥/审计/导出/上传 (agent aa3e9)
总体:防护扎实(出站SSRF双重校验,导出下载路径严,AES-256-GCM,CSV公式注入已处理)。无Critical/High。4中+6低+1信息。
中1 审计写入失败fail-open,回调operator>128字节触发审计丢失(动作已生效). service.go:174-234, external_approval.go:144, model.go:580
中2 凭据脱敏正则遗漏:MASTER_PASSWORD=/conninfo password=/SECRET_ACCESS_KEY明文进审计链+webhook+飞书. redact.go:64,75,96
中3 脚本上传/扫描无大小上限→OOM DoS(make([]byte,file.Size)). terminal.go:213-241,278-300 (对比openapi.go:30有15MB)
中4 敏感字段脱敏可子查询别名绕过含导出路径(绕过"敏感导出需审批"). sensitive.go:55-120,213
低5 外部审批回调secret唯一门禁,省task_id绕交叉校验,ApNo可预测枚举批准,无限流. external_approval.go:106-160, admin.go:461-516
低6 hash脱敏无盐SHA256截断可离线还原(手机号/身份证). sensitive.go:236-238
低7 审计哈希链无密钥/无校验入口/DB层无防篡改,可删改重算. crypto.go:23, service.go:174-234
低8 Webhook/飞书凭据响应回显+deliveries无admin门禁泄露飞书hook URL. admin.go:820,834,707
低9 VELA_SECRET_KEY无强度校验+裸SHA256无盐无KDF;larkSecret/webhook.secret明文;Decrypt无前缀按明文放行. config.go:154-190, cipher.go:51
低10 非prod整体关SSRF防护(staging云主机可打元数据). main.go:83
低11 userDirName对..用户名未拦截(仅admin可设). gateway.go:585-591
信息 ExportJob.Files返回服务器绝对路径. model.go:321
安全点:出站SSRF白名单+拨号期复检防rebinding+169.254(仅Azure168.63.129.16未拦);导出下载Abs+前缀+本人记录;连接密码AES-256-GCM.
