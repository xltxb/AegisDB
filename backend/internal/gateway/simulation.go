package gateway

// 模拟数据的总开关。
//
// 这个仓库里有几条**产出合成数据而不去问目标库**的路径(清单见
// docs/simulated-paths.md)。它们服务的是随产品出厂的演示数据集:没有凭据的连接照样
// 能点开树、跑一条 SELECT、看见结果 —— 本地开发和演示离不开它。
//
// 但它们在生产上是有害的,而且是同一种害法:**把"我们没配凭据"说成"远端的状况"**。
// 一台忘了填密码的生产实例,跑 SELECT 会拿到一份看似合理的假结果,而不是一句"没配
// 凭据";审计链里还会留下一条 executed —— 一条从未接触任何数据库的"已执行"。
//
// 所以:**模拟只在开发环境生效,生产环境一律拒绝。**
//
// ## 默认为 false
//
// 漏设的后果必须是"拒绝",不是"造假"。一个新的入口(将来的 cmd/、一个新的测试夹具、
// 某个工具)如果忘了设这个开关,它得到的是明确的错误,而不是一堆悄悄编出来的数据。
// 这与判定层"查不到规则行 = 放行"的方向相反,是有意的:那一层的默认值是**已知的**
// 妥协,这一层没有理由妥协。
//
// 设置它的地方只有两处,都是显式的:
//   cmd/server/main.go   按 cfg.Env 设置,并把结论打进启动日志
//   bootstrap 的测试夹具 显式打开(它模拟的就是开发环境)
var AllowSimulation = false

// ErrSimulationDisabled 是生产环境下模拟路径给出的回答。
//
// 话要说全两件事:**没配凭据**(问题在这边,不在远端),以及**生产不返回模拟数据**
// (解释为什么开发环境上同样的实例看起来是好的)。少说后半句,人会以为是网关坏了。
var ErrSimulationDisabled = errSimulationDisabled{}

type errSimulationDisabled struct{}

func (errSimulationDisabled) Error() string {
	return "该实例未配置真实执行凭据;生产环境不返回模拟数据"
}

// SimulationAllowed 报告当前是否允许走模拟路径。
//
// 存在这个函数而不是各处直接读变量,是为了让"谁在依赖模拟"可被搜索到 —— 一个裸的
// 布尔量散落在四处,和当初那个没人知道的桩是同一个问题。
func SimulationAllowed() bool { return AllowSimulation }
