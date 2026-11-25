package service

// 卡片的内容与判定里不需要整个网关的那些(.scratch/chat-approval-cards/PRD.md §5.3)。
// 发送、收敛、四类单子的端到端用例在 internal/bootstrap/chat_cards_test.go。

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
)

func cardDB(t *testing.T) (*Services, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cards.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Approval{}, &model.ApprovalStep{}, &model.Setting{}, &model.User{},
		&model.ChatBinding{}, &model.ChatCard{}, &model.ExecWindow{}, &model.ExecWindowDB{}); err != nil {
		t.Fatal(err)
	}
	return &Services{Repo: repository.New(db)}, db
}

// cardTicket 是一张字段齐全的单;mutate 改成各类单子。
func cardTicket(mutate func(*model.Approval)) *model.Approval {
	ap := &model.Approval{
		ID: 1, ApNo: "AP-1024", Instance: "order-cluster", Env: "prod", RiskLevel: model.RiskHigh, Database: "orders_db", TierCode: "prod",
		Command: "DROP TABLE orders_2024_q3;", Reason: "清理过期表", Initiator: "李雷", Status: model.StatusPending,
	}
	if mutate != nil {
		mutate(ap)
	}
	return ap
}

func windowTicket(ap *model.Approval) {
	ap.WindowID = 7
	ap.CommandRef = model.NewRuleRef(model.ApWindowOpen, "name", "夜间班车", "scope", "order-cluster", "db", "orders_db", "reason", "影响大促的理由")
	ap.CommandRef.Parts = []model.RuleRef{*model.NewRuleRef(model.WinOnce, "start", "2026-11-25 22:00", "end", "2026-11-26 02:00")}
	ap.Command = model.RenderApText(ap.CommandRef)
	ap.Reason = "影响大促的理由"
}

func fieldsOf(c chat.Card) map[string]string {
	m := map[string]string{}
	for _, f := range c.Fields {
		m[f.Label] = f.Value
	}
	return m
}

func cardJSON(t *testing.T, c chat.Card) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestChatCard_LarkCarriesEverything(t *testing.T) {
	s, _ := cardDB(t)
	ap := cardTicket(func(a *model.Approval) { a.Notice = "将直接执行,可能长时间锁表" })
	c := s.buildChatCard(ap, chat.Lark, "zh", false, false)
	if c.Statement != ap.Command || c.Reason != ap.Reason || c.Partial || c.Outcome != "" || c.Token != "" || c.Confirming {
		t.Errorf("Lark 卡片: %+v", c)
	}
	if c.Risk != model.RiskHigh || c.Lang != "zh" {
		t.Errorf("风险 / 语言: %q %q", c.Risk, c.Lang)
	}
	for label, want := range map[string]string{"单号": "AP-1024", "类型": "高危命令", "实例": "order-cluster", "数据库": "orders_db", "判定分层": "PROD",
		"环境": "PROD", "风险": "高危", "发起人": "李雷", "注意": "将直接执行,可能长时间锁表"} {
		if got := fieldsOf(c)[label]; got != want {
			t.Errorf("%s = %q,应为 %q", label, got, want)
		}
	}
	// 站内审批页上有的它都有,所以这两处空着时才不出现
	if f := fieldsOf(s.buildChatCard(cardTicket(func(a *model.Approval) { a.Database, a.TierCode = "", "" }), chat.Lark, "zh", false, false)); len(f) != 6 {
		t.Errorf("没有库、没有分层、没有提示时的字段: %v", f)
	}
}

func TestChatCard_TitlePerKind(t *testing.T) {
	s, _ := cardDB(t)
	for _, c := range []struct {
		mutate   func(*model.Approval)
		zh, en   string
		kind, ek string
	}{
		{nil, "高危命令待审批 · AP-1024", "High-risk command awaiting approval · AP-1024", "高危命令", "High-risk command"},
		{func(a *model.Approval) { a.OSCJobID = 3 }, "高危命令待审批 · AP-1024", "High-risk command awaiting approval · AP-1024", "高危命令", "High-risk command"},
		{func(a *model.Approval) { a.ReleaseID = 3 }, "发布待审批 · AP-1024", "Release awaiting approval · AP-1024", "发布", "Release"},
		{windowTicket, "执行窗口待审批 · AP-1024", "Execution window awaiting approval · AP-1024", "执行窗口", "Execution window"},
		{func(a *model.Approval) { a.ExportJobID = 3 }, "敏感导出待审批 · AP-1024", "Sensitive export awaiting approval · AP-1024", "敏感导出", "Sensitive export"},
	} {
		zh, en := s.buildChatCard(cardTicket(c.mutate), chat.Lark, "zh", false, false), s.buildChatCard(cardTicket(c.mutate), chat.Lark, "en", false, false)
		if zh.Title != c.zh || en.Title != c.en {
			t.Errorf("标题 %q / %q,应为 %q / %q", zh.Title, en.Title, c.zh, c.en)
		}
		if fieldsOf(zh)["类型"] != c.kind || fieldsOf(en)["Type"] != c.ek {
			t.Errorf("类型 %q / %q", fieldsOf(zh)["类型"], fieldsOf(en)["Type"])
		}
	}
	// 语句是用户的数据,不跟着语言变
	if c := s.buildChatCard(cardTicket(windowTicket), chat.Lark, "en", false, false); !strings.Contains(c.Statement, "夜间班车") || c.Lang != "en" {
		t.Errorf("英文卡片: %+v", c)
	}
}

// Telegram 默认不带 SQL:只剩固定几项,语句、原因、标题、正文、库名、提示一样都不出现。
func TestChatCard_TelegramWithoutSQL(t *testing.T) {
	s, _ := cardDB(t)
	base := []string{"单号", "类型", "实例", "环境", "风险", "发起人"}
	for name, c := range map[string]struct {
		mutate func(*model.Approval)
		extra  map[string]string
		leaks  []string
	}{
		"命令": {func(a *model.Approval) { a.Notice = "大表 t_big 将直接执行" }, nil, []string{"DROP", "清理过期表", "orders_db", "t_big"}},
		"在线变更": {func(a *model.Approval) { a.OSCJobID = 3; a.Command = "ALTER TABLE t_big ADD INDEX i(c)" },
			map[string]string{"执行方式": "在线变更"}, []string{"ALTER", "t_big"}},
		"脚本": {func(a *model.Approval) { a.ScriptUploadID = 9; a.Command = "\\i fix.sql\nDELETE FROM t" }, nil, []string{"fix.sql", "DELETE"}},
		"发布单": {func(a *model.Approval) {
			a.ReleaseID = 4
			a.Command = releaseApprovalLabel(&model.Release{RelNo: "REL-9", Title: "双十一改价", Instance: "i", Database: "d"}, "UPDATE price SET p=1")
			a.Reason = "紧急 · 发布单 REL-9"
		}, nil, []string{"双十一改价", "UPDATE", "紧急"}},
		"窗口": {windowTicket, map[string]string{"窗口": "夜间班车", "时间段": "一次性 2026-11-25 22:00 → 2026-11-26 02:00"},
			[]string{"影响大促的理由", "orders_db", "开启执行窗口"}},
		"导出原值": {func(a *model.Approval) {
			a.ExportJobID = 5
			a.Command, a.Reason = exportCommand(false, "SELECT id_card FROM t"), "导出包含敏感字段的原始值"
		}, map[string]string{"导出内容": "原值"}, []string{"SELECT", "id_card", "原始值"}},
		"导出原值而 SQL 里写着脱敏字样": {func(a *model.Approval) {
			a.ExportJobID = 5
			a.Command = exportCommand(false, "SELECT 1 -- "+exportMaskedPrefix)
		}, map[string]string{"导出内容": "原值"}, []string{"SELECT"}},
		"导出按脱敏": {func(a *model.Approval) {
			a.ExportJobID = 5
			a.Command, a.Reason = exportCommand(true, "SELECT x FROM t -- [含敏感字段(原值)]"), "引用了敏感字段 id_card"
		}, map[string]string{"导出内容": "按脱敏导出"}, []string{"SELECT", "id_card"}},
	} {
		t.Run(name, func(t *testing.T) {
			ap := cardTicket(c.mutate)
			card := s.buildChatCard(ap, chat.Telegram, "zh", false, false)
			if card.Statement != "" || card.Reason != "" || !card.Partial {
				t.Errorf("不带 SQL 的卡片: %+v", card)
			}
			want := len(base) + len(c.extra)
			got := fieldsOf(card)
			for _, l := range base {
				if got[l] == "" {
					t.Errorf("缺字段 %s", l)
				}
			}
			for l, v := range c.extra {
				if got[l] != v {
					t.Errorf("%s = %q,应为 %q", l, got[l], v)
				}
			}
			if len(card.Fields) != want {
				t.Errorf("字段多了或少了: %v", card.Fields)
			}
			raw := cardJSON(t, card)
			for _, leak := range c.leaks {
				if strings.Contains(raw, leak) {
					t.Errorf("卡片带出了 %q: %s", leak, raw)
				}
			}
		})
	}
}

func TestChatCard_Partial(t *testing.T) {
	s, _ := cardDB(t)
	long := strings.Repeat("汉", 3001)
	for name, c := range map[string]struct {
		mutate    func(*model.Approval)
		truncated bool
		want      bool
	}{
		"短语句":        {nil, false, false},
		"恰好 3000 字":  {func(a *model.Approval) { a.Command = long[:3000*len("汉")] }, false, false},
		"3001 字":     {func(a *model.Approval) { a.Command = long }, false, true},
		"脚本单":        {func(a *model.Approval) { a.ScriptUploadID = 1 }, false, true},
		"单子存的就是截断内容": {func(a *model.Approval) { a.ReleaseID = 1 }, true, true},
		"发布单没截断":     {func(a *model.Approval) { a.ReleaseID = 1 }, false, false},
		// 站内审批页上还有数据量与前置检查的提示,卡片上没有
		"在线变更单":  {func(a *model.Approval) { a.OSCJobID = 3 }, false, true},
		"窗口单":    {windowTicket, false, false},
		"只因遮掉口令": {func(a *model.Approval) { a.Command = "CREATE USER a IDENTIFIED BY 'p@ss'" }, false, false},
	} {
		for _, ch := range chat.Channels { // Telegram 开了带 SQL,和 Lark 同一套判断
			card := s.buildChatCard(cardTicket(c.mutate), ch, "zh", c.truncated, true)
			if card.Partial != c.want {
				t.Errorf("%s/%s: Partial = %v", name, ch, card.Partial)
			}
			if n := utf8.RuneCountInString(card.Statement); n == 0 || n > 3000 || !utf8.ValidString(card.Statement) {
				t.Errorf("%s/%s: 语句 %d 字", name, ch, n)
			}
			if strings.Contains(cardJSON(t, card), "p@ss") {
				t.Errorf("%s/%s: 口令上了卡片", name, ch)
			}
		}
	}
	// 先遮口令再截断:反过来会切在口令中间,剩下的半截认不出来
	cut := strings.Repeat("x", 2975) + " IDENTIFIED BY 'SuperLongSecretValue'"
	if c := s.buildChatCard(cardTicket(func(a *model.Approval) { a.Command = cut }), chat.Lark, "zh", false, false); strings.Contains(c.Statement, "Super") {
		t.Errorf("截断把口令露出来了: …%s", c.Statement[2970:])
	}
}

func TestChatCard_DetailURL(t *testing.T) {
	s, _ := cardDB(t)
	if c := s.buildChatCard(cardTicket(nil), chat.Lark, "zh", false, false); c.DetailURL != "" {
		t.Errorf("没配 consoleURL 不该有详情链接: %q", c.DetailURL)
	}
	if err := s.Repo.SetSetting("notify.consoleURL", `"https://db.example.com/"`); err != nil {
		t.Fatal(err)
	}
	if c := s.buildChatCard(cardTicket(nil), chat.Lark, "zh", false, false); c.DetailURL != "https://db.example.com/#/approvals" {
		t.Errorf("详情链接 %q", c.DetailURL)
	}
}

// 认不出的风险级别原样显示,不露出词条键名。
func TestChatCard_UnknownRisk(t *testing.T) {
	s, _ := cardDB(t)
	c := s.buildChatCard(cardTicket(func(a *model.Approval) { a.RiskLevel = "odd" }), chat.Lark, "zh", false, false)
	if fieldsOf(c)["风险"] != "odd" {
		t.Errorf("风险 = %q", fieldsOf(c)["风险"])
	}
}

// 截没截断与审批单上那份正文必须出自同一条规则。
func TestReleaseLabelTruncated(t *testing.T) {
	inline, script := &model.Release{RelNo: "R"}, &model.Release{RelNo: "R", ScriptUploadID: 1}
	for name, c := range map[string]struct {
		rel  *model.Release
		body string
		want bool
	}{
		"短正文":                 {inline, "SELECT 1", false},
		"恰好 4000":             {inline, strings.Repeat("a", 4000), false},
		"4001":                {inline, strings.Repeat("a", 4001), true},
		"超过 4000 字节不到 4000 字": {inline, strings.Repeat("汉", 2000), false},
		"4001 个汉字":            {inline, strings.Repeat("汉", 4001), true},
		"脚本 800":              {script, strings.Repeat("a", 800), false},
		"脚本 801":              {script, strings.Repeat("a", 801), true},
		"脚本空白折叠后 800":         {script, strings.Repeat("a", 800) + "\n\n   ", false},
		"遮掉口令后不超":             {inline, "ALTER USER u IDENTIFIED BY '" + strings.Repeat("s", 4000) + "'", false},
	} {
		if got := releaseLabelTruncated(c.rel, c.body); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
		// 与标签里实际存下的对得上
		if got := strings.HasSuffix(releaseApprovalLabel(c.rel, c.body), "…"); got != c.want {
			t.Errorf("%s: 标签截断 = %v", name, got)
		}
	}
}

func TestChatErrText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                  "plain",
		"a\r\nb\x00c\x1b[31md e": "a  b c [31md e",
		"  x\n":                  "x",
		"bad\xffutf8":            "badutf8",
	} {
		if got := chatErrText(errors.New(in)); got != want {
			t.Errorf("%q → %q,应为 %q", in, got, want)
		}
	}
	if n := utf8.RuneCountInString(chatErrText(errors.New(strings.Repeat("长", 400)))); n != 255 {
		t.Errorf("应截到列宽,实际 %d", n)
	}
}

// stuckChat 是不理 ctx 的渠道:一调就再也不回来。
type stuckChat struct {
	chat.Transport
	calls chan struct{}
}

func (stuckChat) BotID() string { return "stuck" }
func (c stuckChat) Send(context.Context, string, chat.Card) (chat.Ref, error) {
	c.calls <- struct{}{}
	select {}
}

// 渠道不理超时也不能把扫描挂死;超时之后这一轮不再往这个渠道发。
func TestSweepChatCards_StuckTransport(t *testing.T) {
	old := chatCallTimeout
	chatCallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { chatCallTimeout = old })
	s, db := cardDB(t)
	stuck := stuckChat{calls: make(chan struct{}, 10)}
	s.SetChatTransport(chat.Lark, stuck)
	u := model.User{Name: "a", Email: "a@x", Status: "active"}
	for _, v := range []any{&u, cardTicket(nil), cardTicket(func(a *model.Approval) { a.ID, a.ApNo = 2, "AP-2" })} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.ChatBinding{UserID: u.ID, Channel: "lark", BotID: "stuck", AccountID: "ou"}).Error; err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 2; id++ {
		if err := db.Create(&model.ChatCard{ApprovalID: id, UserID: u.ID, Channel: "lark", State: model.ChatCardQueued, NextAt: time.Now().Add(-time.Second)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { s.SweepChatCards(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("扫描被不理超时的渠道挂死了")
	}
	if n := len(stuck.calls); n != 1 {
		t.Errorf("超时之后这一轮不该再发,实际调了 %d 次", n)
	}
	var row model.ChatCard
	if err := db.First(&row, "approval_id = 1").Error; err != nil {
		t.Fatal(err)
	}
	if row.State != model.ChatCardQueued || row.Attempts != 1 || !strings.Contains(row.LastError, "deadline") {
		t.Errorf("超时算一次失败: %+v", row)
	}
}

// 原因也是上卡片的文字,口令一样要遮。
func TestChatCard_ReasonRedacted(t *testing.T) {
	s, _ := cardDB(t)
	c := s.buildChatCard(cardTicket(func(a *model.Approval) { a.Reason = "按 IDENTIFIED BY 'p@ss' 建号" }), chat.Lark, "zh", false, false)
	if strings.Contains(c.Reason, "p@ss") || !strings.Contains(c.Reason, "建号") {
		t.Errorf("原因: %q", c.Reason)
	}
}

// quietChat 是一碰就算失败的渠道。
type quietChat struct {
	chat.Transport
	t *testing.T
}

func (q quietChat) BotID() string { q.t.Error("排队时碰了渠道"); return "bot" }

// 排队不碰渠道;重复排同一张单不报错、不多出行;已经有结论的单不排。
func TestQueueChatCards(t *testing.T) {
	s, db := cardDB(t)
	s.SetChatTransport(chat.Lark, quietChat{t: t})
	u := model.User{Name: "a", Email: "a@x", Status: "active"}
	pending, decided := cardTicket(nil), cardTicket(func(a *model.Approval) { a.ID, a.ApNo, a.Status = 2, "AP-2", model.StatusApproved })
	for _, v := range []any{&u, pending, decided} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []any{
		&model.ChatBinding{UserID: u.ID, Channel: "lark", BotID: "b", AccountID: "ou"},
		&model.ChatBinding{UserID: u.ID, Channel: "telegram", BotID: "b", AccountID: "1"}, // 这个渠道关着
		&model.ApprovalStep{ApprovalID: 1, ApproverID: u.ID, Status: "active"},
		&model.ApprovalStep{ApprovalID: 1, ApproverID: u.ID, Status: "waiting"},
		&model.ApprovalStep{ApprovalID: 2, ApproverID: u.ID, Status: "approved"},
	} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		s.queueChatCards(pending, true)
		s.queueChatCards(decided, false)
		s.queueChatCards(nil, false)
	}
	var rows []model.ChatCard
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ApprovalID != 1 || rows[0].Channel != "lark" || rows[0].BotID != "b" || !rows[0].SourceTruncated ||
		rows[0].State != model.ChatCardQueued || rows[0].TokenHash != "" {
		t.Errorf("排队的行: %+v", rows)
	}
	if err := s.Repo.QueueChatCards([]model.ChatCard{{ApprovalID: 1, UserID: u.ID, Channel: "lark", NextAt: time.Now()}}); err != nil {
		t.Errorf("重复排队应跳过而不是报错: %v", err)
	}
}

// 在线变更单写明执行方式:只写一句 ALTER 的话,看不出它不是直接执行。
func TestChatCard_OnlineChangeTag(t *testing.T) {
	s, _ := cardDB(t)
	osc := cardTicket(func(a *model.Approval) { a.OSCJobID = 3 })
	for lang, want := range map[string][2]string{"zh": {"执行方式", "在线变更"}, "en": {"Runs as", "Online change"}} {
		for _, ch := range chat.Channels {
			if got := fieldsOf(s.buildChatCard(osc, ch, lang, false, false))[want[0]]; got != want[1] {
				t.Errorf("%s/%s: %q", ch, lang, got)
			}
		}
	}
	if _, has := fieldsOf(s.buildChatCard(cardTicket(nil), chat.Lark, "zh", false, false))["执行方式"]; has {
		t.Error("普通命令不该有这一项")
	}
}

// 系统自己的话跟着卡片语言走;中文就是单子上存的那一句。
func TestChatCard_SystemTextFollowsLanguage(t *testing.T) {
	s, _ := cardDB(t)
	for code, c := range map[string]struct {
		args []string
		en   string
	}{
		model.WinOnce:         {[]string{"start", "A", "end", "B"}, "One-off A → B"},
		model.WinOnceUnset:    {nil, "One-off (time not set)"},
		model.WinDaily:        {[]string{"start", "A", "end", "B", "tz", "Z"}, "Recurring daily A-B (Z)"},
		model.WinWeekly:       {[]string{"days", "1,5", "start", "A", "end", "B", "tz", "Z"}, "Recurring on weekdays 1,5 (1 = Mon) A-B (Z)"},
		model.WinUnknownModel: {nil, "(unknown schedule type)"},
	} {
		ref := model.NewRuleRef(code, c.args...)
		ap := cardTicket(func(a *model.Approval) { windowTicket(a); a.CommandRef.Parts = []model.RuleRef{*ref} })
		if got := fieldsOf(s.buildChatCard(ap, chat.Telegram, "en", false, false))["Schedule"]; got != c.en {
			t.Errorf("%s en = %q", code, got)
		}
		if got := fieldsOf(s.buildChatCard(ap, chat.Telegram, "zh", false, false))["时间段"]; got != model.RenderApText(ref) || got == "" {
			t.Errorf("%s zh = %q,单子上是 %q", code, got, model.RenderApText(ref))
		}
	}
	d := OSCDecision{Table: "t", Rows: 12345, Bytes: 1 << 30}
	d.Parsed.Unsupported = "WHY"
	ap := cardTicket(func(a *model.Approval) { a.Notice, a.NoticeRef = bigDirectNotice(d) })
	if got := fieldsOf(s.buildChatCard(ap, chat.Lark, "zh", false, false))["注意"]; got != ap.Notice {
		t.Errorf("zh 提示 %q", got)
	}
	if got := fieldsOf(s.buildChatCard(ap, chat.Lark, "en", false, false))["Notice"]; !strings.HasPrefix(got, "Table t has about 12,345 rows / ") || !strings.Contains(got, "WHY") {
		t.Errorf("en 提示 %q", got)
	}
	// 认不出的 code 退回单子上存的原句
	ap.NoticeRef = model.NewRuleRef("someNewCode")
	if got := fieldsOf(s.buildChatCard(ap, chat.Lark, "en", false, false))["Notice"]; got != ap.Notice {
		t.Errorf("认不出的提示 %q", got)
	}
}

// 窗口读不出来不等于窗口删了:报错等下一轮,不写下猜出来的结论。
func TestChatOutcome_WindowReadError(t *testing.T) {
	s, db := cardDB(t)
	ap := cardTicket(func(a *model.Approval) { windowTicket(a); a.Status = model.StatusCancelled })
	if text, _, err := s.chatOutcome(ap, "zh"); err != nil || text != "⚪ 已撤回" {
		t.Errorf("窗口删了: %q %v", text, err)
	}
	if err := db.Migrator().DropTable(&model.ExecWindow{}); err != nil {
		t.Fatal(err)
	}
	if text, _, err := s.chatOutcome(ap, "zh"); err == nil {
		t.Errorf("窗口读不出来却写了结论: %q", text)
	}
}
