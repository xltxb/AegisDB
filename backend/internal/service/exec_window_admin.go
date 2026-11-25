package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/model"
)

// 执行窗口的增删改。
//
// 建和改都**走审批**:窗口一次性地把闸门打开一段时间,一个人就能打开这样一扇门,
// 等于给了他一条"先开窗口、再从窗口里进去"的路(理由详见 exec_window_approval.go)。
// 删除不走 —— 关一扇门永远不需要第二个人同意。
//
// 每一次变动都写审计:这扇门什么时候被谁申请、被谁打开过,是事后唯一能回答"当时
// 凭什么不用审批"的依据。

// ErrWindowInvalid 是窗口定义本身说不通,而不是权限或找不到。
var ErrWindowInvalid = apimsg.Errorf("窗口定义无效")

// ErrWindowSpan 是一次性窗口的时段说不通:结束时间已过,或者长过 MaxOnceWindowSpan。
// ErrWindowTooManyDBs:一张窗口勾的库超过 MaxWindowDatabases。
var ErrWindowTooManyDBs = apimsg.Errorf("一张窗口最多勾选 %d 个库", MaxWindowDatabases)

var ErrWindowSpan = apimsg.Errorf("一次性窗口的结束时间须晚于当前时间,且时长不超过 31 天")

// MaxOnceWindowSpan 是一次性窗口的最长时长。
const MaxOnceWindowSpan = 31 * 24 * time.Hour

func (s *Services) ListExecWindows() ([]model.ExecWindow, error) {
	return s.ListExecWindowsFor(nil, true)
}

// ListExecWindowsFor:all=true 是管理后台的全局列表(所有人的窗口,带申请人姓名,只给平台管理员 ——
// 由调用方校验);否则是用户后台的列表,只列 u 自己申请的(管理后台拆分,工单 07)。
//
// 从前列表对所有人开放全量,理由是"一扇免审批的门开在哪,不该只有管理员知道"。拆分之后改为
// 各看各的,全局视图收进管理后台;门放行过什么,仍能在审计与窗口回看里查到。
func (s *Services) ListExecWindowsFor(u *model.User, all bool) ([]model.ExecWindow, error) {
	ws, err := s.Repo.ListExecWindows()
	if err != nil {
		return nil, err
	}
	if !all {
		own := ws[:0]
		for _, w := range ws {
			if u != nil && w.CreatedBy == u.ID {
				own = append(own, w)
			}
		}
		ws = own
	} else {
		names := map[int64]string{}
		for i := range ws {
			id := ws[i].CreatedBy
			if _, ok := names[id]; !ok {
				if usr, err := s.Repo.GetUserByID(id); err == nil {
					names[id] = usr.Name
				} else {
					names[id] = ""
				}
			}
			ws[i].CreatedByName = names[id]
		}
	}
	// Active 要和**判定层放不放行**说同一件事,所以它也要看审批状态。
	//
	// 判定那一路的 status 过滤在 SQL 里(ExecWindowsFor),这一路是全量列表,过滤不
	// 在查询里 —— 少了这一句,一张还在等审批的窗口会在总览页上显示成"正开着",而它
	// 一行都放行不了。那是这个界面能犯的最糟的一种错:它谎报的正是"门此刻开着吗"。
	now := time.Now()
	for i := range ws {
		ws[i].Active = ws[i].Status == model.WindowApproved && windowCovers(&ws[i], now)
		// Expired 只描述时间表,所以**不**看 status —— 理由见 model.ExecWindow.Expired。
		ws[i].Expired = windowExpired(&ws[i], now)
	}
	return ws, nil
}

// ListActiveExecWindows:此刻开着的窗口,所有人的(带申请人姓名)。给用户后台的总览用 ——
// 一扇免审批的门此刻开在哪个库上,在那台库上干活的人都该知道,不只是申请人自己
// (2026-10-14 用户确认)。只列开着的;待审批、排队、已过期的仍各看各的(见 ListExecWindowsFor)。
func (s *Services) ListActiveExecWindows() ([]model.ExecWindow, error) {
	ws, err := s.ListExecWindowsFor(nil, true)
	if err != nil {
		return nil, err
	}
	open := ws[:0]
	for _, w := range ws {
		if w.Active {
			open = append(open, w)
		}
	}
	return open, nil
}

// ListReviewableExecWindows:用户后台班车页的「全部窗口」页签(2026-10-26 回归排查 08)—— 有回看权限
// (canReviewWindow)的人复核别人的班车放行过什么,含已过期的,带申请人姓名,只读。
//
// 拆分之后列表各看各的,回看按钮挂在列表行上,监督角色和 L2 就再也点不到别人那扇门的回看
// (后端 canReviewWindow 一直为此开着)。这里只列**点得开回看**的那些,与 WindowStatements 同一口径:
//   - 监督角色(admin / owner / audit):全部;
//   - 其他有 execwindow 菜单的人:自己申请的,加上够得到那台实例、且已批准的别人的窗口 ——
//     够不到的实例上的窗口不列(回看那边回"不存在",列出来等于泄露窗口与实例);未批准的别人的
//     窗口回看不了,也不列。
//
// 没有回看权限:ErrForbidden。
func (s *Services) ListReviewableExecWindows(u *model.User) ([]model.ExecWindow, error) {
	if !s.canReviewWindow(u) {
		return nil, ErrForbidden
	}
	ws, err := s.ListExecWindowsFor(nil, true)
	if err != nil {
		return nil, err
	}
	if s.canSeeAllActivity(u) {
		return ws, nil
	}
	reach := map[int64]bool{}
	out := ws[:0]
	for _, w := range ws {
		if w.CreatedBy == u.ID {
			out = append(out, w)
			continue
		}
		if w.Status != model.WindowApproved || w.DecidedAt == nil {
			continue
		}
		ok, seen := reach[w.ConnectionID]
		if !seen {
			ok = s.windowConnReachable(u, w.ConnectionID)
			reach[w.ConnectionID] = ok
		}
		if ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// ListScheduledExecWindows:已批准、启用中、还没到点也没开完的窗口,所有人的(带申请人姓名)。
// 给用户后台总览的「执行窗口排期」用 —— 今晚 / 这周哪个库会有一段免审批时间,在那台库上干活
// 的人该提前知道,与「生效中」同一口径(2026-10-19 用户确认)。此刻开着的归 ListActiveExecWindows;
// 待审批的申请仍只给申请人自己看。
func (s *Services) ListScheduledExecWindows() ([]model.ExecWindow, error) {
	ws, err := s.ListExecWindowsFor(nil, true)
	if err != nil {
		return nil, err
	}
	queued := ws[:0]
	for _, w := range ws {
		if w.Status == model.WindowApproved && w.Enabled && !w.Active && !w.Expired {
			queued = append(queued, w)
		}
	}
	return queued, nil
}

func (s *Services) CreateExecWindow(actor *model.User, req dto.ExecWindowReq) (*model.ExecWindow, error) {
	w := &model.ExecWindow{}
	if err := fillExecWindow(w, req); err != nil {
		return nil, err
	}
	if !s.windowConnReachable(actor, w.ConnectionID) {
		return nil, ErrNotFound
	}
	w.CreatedBy = actor.ID
	// 先落成 pending 再建单:单子里要写窗口 ID,而 ID 要等这一行插进去才有。
	// 这个顺序下最坏的中间态是"有窗口、没单子",而那种窗口是 pending —— 不放行
	// 任何东西。反过来先建单则会留下一张指向不存在窗口的单。
	w.Status = model.WindowPending
	if err := s.Repo.CreateExecWindow(w); err != nil {
		return nil, err
	}
	if err := s.raiseWindowApproval(actor, w); err != nil {
		// 建单失败就把窗口收回去,不留一个永远等不到审批的 pending 行。
		_ = s.Repo.DeleteExecWindow(w.ID)
		return nil, err
	}
	return w, nil
}

// UpdateExecWindow 改一个窗口 —— 改完**重新走审批**。
//
// 因为"把 02:00-04:00 改成 02:00-06:00"和"新开一扇 04:00-06:00 的门"是同一件事,
// 而后者要签字。如果改动不重审,那审批就只拦得住第一版,任何人都能在批准之后把
// 时间段拉长、把库换掉 —— 那扇门上签的字就不再对应它现在的样子了。
func (s *Services) UpdateExecWindow(actor *model.User, id int64, req dto.ExecWindowReq) (*model.ExecWindow, error) {
	w, err := s.Repo.GetExecWindow(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canManageWindow(actor, w) {
		return nil, ErrForbidden
	}
	if err := fillExecWindow(w, req); err != nil {
		return nil, err
	}
	if !s.windowConnReachable(actor, w.ConnectionID) {
		return nil, ErrNotFound
	}
	oldTicket := w.ApprovalID
	// 回到待审批:从这一刻起它不再放行任何东西,直到新的单子被批准。
	w.Status = model.WindowPending
	w.ApprovalID, w.ApNo, w.DecidedAt = 0, "", nil
	if err := s.Repo.UpdateExecWindow(w); err != nil {
		return nil, err
	}
	// 旧单作废:它描述的是改之前的定义。不撤的话它仍在待办里,批准它会让**改过之后**
	// 的窗口生效(2026-10 黑盒,工单 01 第 2 条)。批准那一路另有一道:只认窗口当前挂着
	// 的单(windowTicketCurrent)—— 窗口上的单号此刻已经清空,旧单批不动了。
	s.withdrawWindowTicket(actor, oldTicket, apimsg.Text("执行窗口已修改,旧申请作废"))
	if err := s.raiseWindowApproval(actor, w); err != nil {
		return nil, err
	}
	s.auditWindowChange(actor, w, "修改(重新提交审批)")
	return w, nil
}

// canManageWindow —— 改或撤一个窗口,只有申请人自己或平台管理员可以。
//
// 不加这条的后果不是"别人乱改",而是一条绕过审批的路:任何拿到菜单的人都能把一张
// 已批准的窗口改成自己要的时间段和库,然后以自己的名义重新提交 —— 那扇门上原来
// 签的字就不再对应它现在的样子了。
func (s *Services) canManageWindow(actor *model.User, w *model.ExecWindow) bool {
	if actor == nil {
		return false
	}
	if w.CreatedBy == actor.ID {
		return true
	}
	for _, code := range s.Repo.RoleCodesForIDs(s.Repo.EffectiveRoleIDs(actor)) {
		if code == "admin" {
			return true
		}
	}
	return false
}

// DisableExecWindow 停用一个窗口:立即不再放行任何东西,不走审批 —— 关门是收紧,不需要谁签字。
// 申请人自己或平台管理员可以(管理后台的全局管理页用它停别人的窗口,工单 07)。停用写审计。
// 重新启用不在这里:放宽要重新提交审批(UpdateExecWindow)。
func (s *Services) DisableExecWindow(actor *model.User, id int64) (*model.ExecWindow, error) {
	w, err := s.Repo.GetExecWindow(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canManageWindow(actor, w) {
		return nil, ErrForbidden
	}
	// 只改 enabled 一列(见 Repo.DisableExecWindow):不会覆盖申请人同时做的修改。
	changed, err := s.Repo.DisableExecWindow(id)
	if err != nil {
		return nil, err
	}
	if fresh, ferr := s.Repo.GetExecWindow(id); ferr == nil {
		w = fresh
	}
	if changed { // 已经是停用的(或别人先停了):不重复记审计
		s.auditWindowChange(actor, w, "停用")
	}
	return w, nil
}

func (s *Services) DeleteExecWindow(actor *model.User, id int64) error {
	w, err := s.Repo.GetExecWindow(id)
	if err != nil {
		return ErrNotFound
	}
	if !s.canManageWindow(actor, w) {
		return ErrForbidden
	}
	if err := s.Repo.DeleteExecWindow(id); err != nil {
		return err
	}
	// 还在等审批的单一起撤回:窗口都没了,单子留在待办里只会让人批一张空单,
	// 还回一句"执行窗口已生效"(2026-10 黑盒,工单 01 第 3 条)。
	s.withdrawWindowTicket(actor, w.ApprovalID, apimsg.Text("执行窗口已删除,申请撤回"))
	s.auditWindowChange(actor, w, "删除")
	return nil
}

// windowConnReachable —— 申请人够得到这台实例,才能为它申请 / 改窗口。与 /terminal/exec
// 同一道闸(canAccessConn)。否则一个够不到某台实例的人,能为它开一扇门、批下来之后放行
// 别人的语句,自己还能看那扇门的回看(2026-10 黑盒,工单 01 第 4 条)。
//
// 不存在与够不到回同一个结果:否则靠枚举 connectionId 就能问出哪些实例存在。
func (s *Services) windowConnReachable(actor *model.User, connID int64) bool {
	conn, err := s.Repo.GetConnection(connID)
	return err == nil && s.canAccessConn(actor, conn)
}

// auditWindowChange 把窗口的变动写进审计链。
//
// 开一扇免审批的门是一次权限变更,和改能力矩阵、改高危字典是同一类事,所以它进的是
// 同一条不可篡改的链,而不是某个日志文件。
func (s *Services) auditWindowChange(actor *model.User, w *model.ExecWindow, verb string) {
	conn, _ := s.Repo.GetConnection(w.ConnectionID)
	s.recordAudit(actor, conn,
		verb+"执行窗口「"+w.Name+"」· "+describeWindow(w)+" · 理由:"+w.Reason,
		model.RiskHigh, model.ResultExecuted, "", "")
}

// describeWindow 是给人读的一行摘要,进审计,也给控制台列表用。
func describeWindow(w *model.ExecWindow) string {
	scope := "实例#" + strconv.FormatInt(w.ConnectionID, 10) + "/" + windowDBLabel(w)
	return scope + " · " + windowSchedule(w)
}

// windowSchedule 只讲**时间表**那一半,不带作用域。
//
// 审批摘要要自己拼作用域(那里有实例名,比 "实例#7" 好读),而审计里的
// describeWindow 用的是 ID —— 实例可以改名,而审计要在改名之后仍然指得回去。
func windowSchedule(w *model.ExecWindow) string {
	return model.RenderApText(windowScheduleRef(w))
}

// windowScheduleRef 是同一张时间表的机器可读形式(见 model.RenderApText):审批单把它存下来,
// 界面据此用读者的语言显示;windowSchedule 的中文串由它渲染,两处不会分叉。
func windowScheduleRef(w *model.ExecWindow) *model.RuleRef {
	switch w.Kind {
	case model.WindowOnce:
		if w.StartsAt == nil || w.EndsAt == nil {
			return model.NewRuleRef(model.WinOnceUnset)
		}
		return model.NewRuleRef(model.WinOnce,
			"start", w.StartsAt.Format("2006-01-02 15:04"), "end", w.EndsAt.Format("2006-01-02 15:04"))
	case model.WindowRecurring:
		if days := strings.TrimSpace(w.Weekdays); days != "" {
			return model.NewRuleRef(model.WinWeekly,
				"days", days, "start", minLabel(w.StartMin), "end", minLabel(w.EndMin), "tz", w.Timezone)
		}
		return model.NewRuleRef(model.WinDaily, "start", minLabel(w.StartMin), "end", minLabel(w.EndMin), "tz", w.Timezone)
	}
	return model.NewRuleRef(model.WinUnknownModel)
}

func minLabel(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60%24, m%60)
}

// fillExecWindow 校验并写入一个窗口定义。
//
// 校验在这里从严,因为写坏的窗口有两种烂法,而它们不对等:一种是永远不开(白配),
// 另一种是**开在没预料的时间**。所以宁可拒绝一个说不清的定义,也不替用户猜。
func fillExecWindow(w *model.ExecWindow, req dto.ExecWindowReq) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return ErrWindowInvalid
	}
	dbs, err := windowDBsOf(req)
	if err != nil {
		return err
	}
	if req.ConnectionID <= 0 {
		return ErrWindowInvalid
	}
	w.Name = name
	w.ConnectionID = req.ConnectionID
	w.Database, w.Databases = dbs[0], dbs
	w.Enabled = req.Enabled
	w.Reason = strings.TrimSpace(req.Reason)

	switch req.Kind {
	case model.WindowOnce:
		if req.StartsAt == nil || req.EndsAt == nil || !req.EndsAt.After(*req.StartsAt) {
			return ErrWindowInvalid // 没有起止,或者结束不晚于开始
		}
		// 结束要晚于现在:整段落在过去的窗口一行都放行不了,批下来只是一张空单。
		// 时长有上限:窗口是"受控的免审批",一开几百天等于取消了审批(2026-10 黑盒,工单 01 第 5 条)。
		if !req.EndsAt.After(time.Now()) || req.EndsAt.Sub(*req.StartsAt) > MaxOnceWindowSpan {
			return ErrWindowSpan
		}
		w.Kind = model.WindowOnce
		w.StartsAt, w.EndsAt = req.StartsAt, req.EndsAt
		w.Weekdays, w.StartMin, w.EndMin, w.NotAfter = "", 0, 0, nil
		w.Timezone = "UTC" // 一次性窗口比的是绝对时刻,时区不参与判定
		return nil

	case model.WindowRecurring:
		tz := strings.TrimSpace(req.Timezone)
		if tz == "" {
			return ErrWindowInvalid
		}
		if _, err := time.LoadLocation(tz); err != nil {
			// 存一个判定时会失败的时区,等于存一个永远不开的窗口 —— 当场拒绝,
			// 别让人以为配好了。
			return ErrWindowInvalid
		}
		if req.StartMin < 0 || req.StartMin >= 24*60 || req.EndMin < 0 || req.EndMin >= 24*60 {
			return ErrWindowInvalid
		}
		if req.StartMin == req.EndMin {
			return ErrWindowInvalid // 说不清是零长还是整天
		}
		if err := validWeekdays(req.Weekdays); err != nil {
			return err
		}
		w.Kind = model.WindowRecurring
		w.Timezone = tz
		w.Weekdays = strings.TrimSpace(req.Weekdays)
		w.StartMin, w.EndMin = req.StartMin, req.EndMin
		w.NotAfter = req.NotAfter
		w.StartsAt, w.EndsAt = nil, nil
		return nil
	}
	return ErrWindowInvalid
}

// MaxWindowDatabases:一张窗口最多勾几个库。窗口是"受控的免审批",勾得越多放开面越大,
// 审批人也越难看清自己签的是什么。
const MaxWindowDatabases = 20

// windowDBsOf 取出这次申请要放行的库:Databases(多选)优先,老客户端只传 Database 也认。
// 去空白、去重(保持勾选顺序);一个都没有就拒绝 —— 库名必填,空库名会让一个窗口悄悄覆盖整台实例。
func windowDBsOf(req dto.ExecWindowReq) ([]string, error) {
	src := req.Databases
	if len(src) == 0 {
		src = []string{req.Database}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range src {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		if len([]rune(d)) > 128 {
			return nil, ErrWindowInvalid
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, ErrWindowInvalid
	}
	if len(out) > MaxWindowDatabases {
		return nil, ErrWindowTooManyDBs
	}
	return out, nil
}

// windowDBLabel 是给人读的库列表(审计、审批摘要)。
func windowDBLabel(w *model.ExecWindow) string { return strings.Join(w.DBs(), "、") }

// validWeekdays 校验班次:空串是每天,否则必须是 1..7 的逗号列表。
// 写坏的班次会被 weekdayAllowed 当成"哪天都不发车",配的人却以为配好了。
func validWeekdays(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 7 {
			return ErrWindowInvalid
		}
	}
	return nil
}

// IsPlatformAdmin:多角色并集里含平台管理员(与 middleware.AdminOnly 同一口径)。
func (s *Services) IsPlatformAdmin(u *model.User) bool {
	if u == nil {
		return false
	}
	for _, code := range s.Repo.RoleCodesForIDs(s.Repo.EffectiveRoleIDs(u)) {
		if code == "admin" {
			return true
		}
	}
	return false
}
