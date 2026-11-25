package chat

// Field 是卡片上的一行「标签:值」。
type Field struct{ Label, Value string }

// Card 是一张与平台无关的卡片,由各渠道按自己的格式画出来。
//
// 里面全是纯文本:Statement 已经遮过口令(sqlutil.RedactSecrets),但没有做任何平台的转义 ——
// 转义是渠道的事,只有它知道自己的标记语法。
type Card struct {
	Title     string
	Risk      string // high | mid | low
	Lang      string // zh | en,见 Tr
	DetailURL string // 空 = 没有「查看详情」
	// Token 是这张卡片的随机令牌,按钮只带它和动作。
	Token string

	Fields    []Field
	Statement string // 空 = 这个渠道不带语句
	Reason    string // 发起人填的原因

	Partial    bool   // 卡片上不是完整内容:正文和通过的二次确认都要写明
	Confirming bool   // 正在二次确认
	Outcome    string // 非空 = 已结束,没有按钮
	Test       bool   // 测试卡片
}
