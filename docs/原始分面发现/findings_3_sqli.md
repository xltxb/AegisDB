# 攻击面3 SQL注入穿透/拦截绕过/命令执行 (agent a1cfb)
总体:核心拦截稳健(Exec无条件SplitStatements+strictestVerdict);无命令注入(全仓无os/exec子进程)。2 High + 1 Medium。
[高1] ExecAsync对单语句判定原文而非拆分文本→ro绕能力矩阵在PROD写库. async_exec.go:52-55(仅len>1才拆分) vs gateway.go:119-123(无条件拆分). 根因risk.go:463-475 MapVerbToCapability("")→select. payload: ";UPDATE users SET is_admin=1" 经SplitStatements归一len==1保留原文判定,ParseVerb首字符;→空动词→select→ro prod allow→入队执行原串. 高危字典不含INSERT/UPDATE/CREATE. **需核实**
[高2] SessionScoped未识别MySQL SET @@GLOBAL.写法→ro改服务器全局变量. session_scope.go:37 setEscapesSessionRe只认关键字SET GLOBAL不认@@GLOBAL. risk.go:638-650短路. payload "SET @@GLOBAL.read_only=0"→SessionScoped=true→仅需select→allow→执行. 对Exec同步路径也成立. **需核实**
[中3] 高危字典多词条目对空白/注释分隔敏感(regexp.QuoteMeta字面单空格),管理员自定义多词规则如"FLUSH PRIVILEGES"可被多空格/Tab绕过. risk.go:498-505. 默认种子无多词条目故仅配置相关.
安全点:同步Exec堆叠查询逐条拦;DSN multiStatements=false;注释解析方向安全;导出只读闸;对象浏览参数化+白名单.
