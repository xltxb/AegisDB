package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
var ErrWindowInvalid = fmt.Errorf("窗口定义无效")

func (s *Services) ListExecWindows() ([]model.ExecWindow, error) {
	ws, err := s.Repo.ListExecWindows()
	if err != nil {
		return nil, err
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

func (s *Services) CreateExecWindow(actor *model.User, req dto.ExecWindowReq) (*model.ExecWindow, error) {
	w := &model.ExecWindow{}
	if err := fillExecWindow(w, req); err != nil {
		return nil, err
	}
	if _, err := s.Repo.GetConnection(w.ConnectionID); err != nil {
		return nil, ErrNotFound // 窗口挂在一台不存在的实例上,是配错了
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
	if _, err := s.Repo.GetConnection(w.ConnectionID); err != nil {
		return nil, ErrNotFound
	}
	// 回到待审批:从这一刻起它不再放行任何东西,直到新的单子被批准。
	w.Status = model.WindowPending
	w.ApprovalID, w.ApNo, w.DecidedAt = 0, "", nil
	if err := s.Repo.UpdateExecWindow(w); err != nil {
		return nil, err
	}
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
	s.auditWindowChange(actor, w, "删除")
	return nil
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
	scope := "实例#" + strconv.FormatInt(w.ConnectionID, 10) + "/" + w.Database
	return scope + " · " + windowSchedule(w)
}

// windowSchedule 只讲**时间表**那一半,不带作用域。
//
// 审批摘要要自己拼作用域(那里有实例名,比 "实例#7" 好读),而审计里的
// describeWindow 用的是 ID —— 实例可以改名,而审计要在改名之后仍然指得回去。
func windowSchedule(w *model.ExecWindow) string {
	switch w.Kind {
	case model.WindowOnce:
		if w.StartsAt == nil || w.EndsAt == nil {
			return "一次性(时间未设置)"
		}
		return "一次性 " + w.StartsAt.Format("2006-01-02 15:04") + " → " + w.EndsAt.Format("2006-01-02 15:04")
	case model.WindowRecurring:
		days := w.Weekdays
		if strings.TrimSpace(days) == "" {
			days = "每天"
		} else {
			days = "周" + days
		}
		return "班车 " + days + " " + minLabel(w.StartMin) + "-" + minLabel(w.EndMin) + " (" + w.Timezone + ")"
	}
	return "(未知时间模型)"
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
	db := strings.TrimSpace(req.Database)
	if req.ConnectionID <= 0 || db == "" {
		// 库名必填:空库名会让一个窗口悄悄覆盖整台实例。
		return ErrWindowInvalid
	}
	w.Name = name
	w.ConnectionID = req.ConnectionID
	w.Database = db
	w.Enabled = req.Enabled
	w.Reason = strings.TrimSpace(req.Reason)

	switch req.Kind {
	case model.WindowOnce:
		if req.StartsAt == nil || req.EndsAt == nil || !req.EndsAt.After(*req.StartsAt) {
			return ErrWindowInvalid // 没有起止,或者结束不晚于开始
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
