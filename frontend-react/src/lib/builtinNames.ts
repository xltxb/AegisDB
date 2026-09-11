// builtinNames — 出厂自带的发布流程/阶段名字,按读者的语言显示。
//
// 这些中文**不是界面文案,是种子数据**(backend/internal/bootstrap/pipeline_seed.go
// 往 tbl_pipeline / tbl_pipeline_stage 里插的行)。所以它不能像别的文案那样"翻译一下"
// 就完事:同一个字段用户随时可以改,改完之后就没有什么可翻译的了。
//
// 取舍与 envTierLabels 的 BUILTIN_TIER_LABEL 一模一样,那里已经写明了:**随产品出厂
// 的那几行按稳定标识走 i18n,用户自己建的只能显示当初键入的那串** —— 这是把这些东西
// 做成数据所要付的代价。
//
// 标识就是种子里那串中文本身:这两张表没有 code 列,而加一列要动迁移、模型、接口和
// 前端四处。用原串做键还有一个恰好正确的副作用 —— **用户改过名之后自然不再匹配**,
// 于是显示他自己的名字,这正是想要的行为。代价是种子文案一改这张表就失配(表现为
// "又变回中文",看得见、不危险),所以 tests/unit/builtinNames.spec.ts 直接读那个 Go
// 文件来核对。
//
// ## 输入框里的回写陷阱
//
// 这些串出现在**可编辑的输入框**里。要是把翻译塞进 v-model,用户在英文下改一改别的
// 字段、按一次保存,英文名就被写进库了,中文同事再看就全变了 —— 一次纯粹由"用了哪种
// 语言看"引起的数据变更。
//
// 所以输入框用 `:value="label(raw)"` + `@input` 写回原始 ref,而不是 v-model:
// 没动过 → ref 里还是原串 → 保存写回原串;动过一个字符 → ref 变成用户输入的整串 →
// 它不在这张表里 → 之后原样显示、原样保存。两种情况都不需要保存时再判一次。

/** 出厂流程的名字 → 文案键。值取自 pipeline_seed.go 的 defaultPipelines。 */
export const BUILTIN_FLOW_NAME: Record<string, string> = {
  标准发布流程: 'plSeedStd',
  开发自助发布: 'plSeedDevSelf',
}

/** 出厂流程的描述 → 文案键。 */
export const BUILTIN_FLOW_DESC: Record<string, string> = {
  '规范审查 → 人工审批 → 备份点 → 执行 → 校验 → 通知': 'plSeedStdDesc',
  '仅规范审查后直接执行,限开发分层': 'plSeedDevSelfDesc',
}

// 阶段名不在这里:它已经不是数据了。
//
// 阶段名 = 阶段类型 —— 界面上只读地按 plType_* 显示,存进库的那份由服务端按类型给出
// (service/pipeline.go 的 stageTypeLabel)。原先它是个自由文本框,于是同一件事有两个
// 可能对不上的说法,而且谁用哪种语言按过保存,记录里就留下哪种语言。

type Translate = (key: string) => string

/**
 * 按表把出厂串换成读者语言的说法;不在表里的原样返回。
 *
 * 空串也原样返回:一个没填描述的流程显示的是"没填",不是某个默认句子。
 */
export function builtinLabel(map: Record<string, string>, stored: string, t: Translate): string {
  const key = map[(stored || '').trim()]
  return key ? t(key) : stored
}
