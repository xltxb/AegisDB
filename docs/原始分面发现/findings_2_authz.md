# 攻击面2 授权/RBAC/越权 (agent ac95b)
总体:授权面扎实,MenuGuard/AdminOnly fail-closed,IDOR均有owner校验,tag fail-closed。无低权限直接可利用致命洞。
1[中] 不存在角色id被RBAC读作全放行+不限tag(fail-open). repository.go:262(CapabilityLevel查无行→LevelAllow),496(TagsForRoles无tag→unrestricted),risk.go:558(union取最宽松). 触发入口admin.go:280 AddMember不校验角色存在, admin.go:537,558 PATCH users单角色roleId漏validateRoleIDs
2[中] API凭据可绑定任意人类账号(含admin),成免MFA替身. openapi.go:155-159,242-246 缺Kind==service校验; apiclient.go:76-89注入ctxUser
3[中低] 审批链成员是建单快照,角色回收后仍可审批既有工单. gateway.go:346-356 isChainMember, approval_decidability.go:55
4[中低] ExecuteApproved执行时不复核连接访问权/maint/tier(用快照TierCode). approval_execute.go:32-82,51
5[低] Oracle包编译compile绕过prod MFA. oracle_compile.go:24-75 无checkMFA
6[低] /risk/check不校验tag访问权,可探测不可见实例. gateway.go:53-78
7[低] /sensitive-columns全表可见(含不可见实例的库表列名). router.go:155, sensitive.go:70
