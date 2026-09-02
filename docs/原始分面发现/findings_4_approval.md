# 攻击面4 审批链/服务账号/APIclient (agent ab8c0)
总体:设计严谨,原子claim/SHA256复核/自审批默认禁均到位。2中+2中低+3低。
中1 外部审批回调:持callbackSecret即可终审任意待审工单(含未外发),approver自填绕两人控制,ApNo可枚举,allowIPs默认空,?secret=查询回落. external_approval.go:106-168,126,194, admin.go:468 【与攻击面5-#5重复,强信号】
中2 流水线backup/verify模板SQL绕过风险引擎+审批,以发布人身份记审计. pipeline.go:693,698-733,727,907-940
中低3 已批准工单永久有效,执行不复核实例访问/maint/MFA,复判用快照tier. approval_execute.go:32-85,51,88-120 【与攻击面2-#4重复,强信号】
低4 API凭据可绑定任意用户(含admin),服务账号可授admin角色. openapi.go:155-161,241-247 【与攻击面2-#2重复,强信号】
低5 审批链多步名义化:任一成员一票终审,step归因错误,快照不随角色变更失效. gateway.go:346-356, repository.go:966-980 【与攻击面2-#3重复】
低6 /open无限流+每请求bcrypt→CPU DoS,AllowIPs默认空. apiclient.go:48-98
安全点:ExecuteApproved原子claim防重复;自审批默认禁;状态机原子转移;凭据bcrypt+恒时比较;服务账号Login/设密双封闭;默认不信任代理XFF.
