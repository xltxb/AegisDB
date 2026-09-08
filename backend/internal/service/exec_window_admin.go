package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/model"
)

// 执行窗口的增删改。管理员直接建,不走审批 —— 与高危命令字典、能力矩阵同级。
// 但每一次变动都写审计:这扇门什么时候被谁打开过,是事后唯一能回答"当时凭什么不用
// 审批"的依据。

// ErrWindowInvalid 是窗口定义本身说不通,而不是权限或找不到。
var ErrWindowInvalid = fmt.Errorf("窗口定义无效")

func (s *Services) ListExecWindows() ([]model.ExecWindow, error) {
	ws, err := s.Repo.ListExecWindows()
	if err != nil {
		return nil, err
	}
	// 判定用的是同一个 windowCovers,所以界面上的"现在开着"与网关的放行永远一致。
	now := time.Now()
	for i := range ws {
		ws[i].Active = windowCovers(&ws[i], now)
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
	if err := s.Repo.CreateExecWindow(w); err != nil {
		return nil, err
	}
	s.auditWindowChange(actor, w, "创建")
	return w, nil
}

func (s *Services) UpdateExecWindow(actor *model.User, id int64, req dto.ExecWindowReq) (*model.ExecWindow, error) {
	w, err := s.Repo.GetExecWindow(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if err := fillExecWindow(w, req); err != nil {
		return nil, err
	}
	if _, err := s.Repo.GetConnection(w.ConnectionID); err != nil {
		return nil, ErrNotFound
	}
	if err := s.Repo.UpdateExecWindow(w); err != nil {
		return nil, err
	}
	s.auditWindowChange(actor, w, "修改")
	return w, nil
}

func (s *Services) DeleteExecWindow(actor *model.User, id int64) error {
	w, err := s.Repo.GetExecWindow(id)
	if err != nil {
		return ErrNotFound
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
	switch w.Kind {
	case model.WindowOnce:
		if w.StartsAt == nil || w.EndsAt == nil {
			return scope + " · 一次性(时间未设置)"
		}
		return scope + " · 一次性 " + w.StartsAt.Format("2006-01-02 15:04") + " → " + w.EndsAt.Format("2006-01-02 15:04")
	case model.WindowRecurring:
		days := w.Weekdays
		if strings.TrimSpace(days) == "" {
			days = "每天"
		} else {
			days = "周" + days
		}
		return scope + " · 班车 " + days + " " + minLabel(w.StartMin) + "-" + minLabel(w.EndMin) + " (" + w.Timezone + ")"
	}
	return scope
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
