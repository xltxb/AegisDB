# 攻击面1 认证与会话 (agent a61bf)
总体:无Critical/High。3 Medium + 6 Low。
M-1 MFA grace 按 uid:tokenVersion:connID 缓存(非单会话) → 被盗token 30min内搭便车绕过PROD step-up. service.go:353-355, gateway.go:876-883
M-2 自助 MFA setup/enable/disable 无口令重认证+零审计 → 盗token者可劫持未启用MFA账号或解绑因子. gateway.go:917-977, admin.go:542-579
M-3 登录频控仅按源IP+进程内存;口令正确未带码返回42800可区分→单独确认口令. terminal.go:26-57, loginlimit.go:25-32
L-1 登录TOTP不消费计数器 ~90s可重复登录. gateway.go:886-892
L-2 WS长连接不复核JWT过期. terminal.go:529,609-625
L-3 状态改invited不失效已发token(只拦disabled). middleware.go:78, terminal.go:541,614, admin.go:521-535
L-4 server init重置管理员口令不bump代次. init.go:71-78
L-5 TOTP secret明文存储. model.go:170, repository.go:102-105
L-6 弱默认(公开JWT密钥/seed)仅prod拒绝,未知env降级为dev. config.yaml:30, config.go:173-175, seed.go:29-31
