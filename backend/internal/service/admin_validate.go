package service

// 管理后台的输入校验(2026-10 黑盒测试,工单 02)。
//
// 各处从前要么不校验、要么校验了但都报同一句"创建失败" —— 人看了不知道该改哪一项。
// 这里统一一种"说得出哪里不对"的错误:它 Unwrap 成 ErrBadRequest,老的 errors.Is 判断照常
// 生效;handler 认出它时把 Msg 原样回给界面(40001)。

import (
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// InputError 是一条可以原样给人看的校验失败原因。
type InputError struct {
	Msg string
	ref *model.RuleRef // Msg 的 apimsg 身份,界面据此翻译
}

func (e *InputError) Error() string          { return e.Msg }
func (e *InputError) Unwrap() error          { return ErrBadRequest }
func (e *InputError) MsgRef() *model.RuleRef { return e.ref }

func invalidf(format string, args ...any) error {
	err := apimsg.Errorf(format, args...)
	return &InputError{Msg: err.Error(), ref: apimsg.RefOf(err)}
}

// asInputError 把一条 apimsg 错误包成 InputError:原话回给界面(40001),msgRef 照带。
func asInputError(err error) error {
	return &InputError{Msg: err.Error(), ref: apimsg.RefOf(err)}
}

// AsInputError 同 InputErrorMsg,但交出错误本身 —— resp.FailErr 要从它取 msgRef。
func AsInputError(err error) (*InputError, bool) {
	var ie *InputError
	ok := errors.As(err, &ie)
	return ie, ok
}

// InputErrorMsg 取出校验失败的原因;不是校验失败时 ok=false。
func InputErrorMsg(err error) (string, bool) {
	var ie *InputError
	if errors.As(err, &ie) {
		return ie.Msg, true
	}
	return "", false
}

// 姓名、邮箱的长度上限,与 tbl_user 的列宽一致(name 64、email 128)。邮箱按 RFC 5321 本可到 254,
// 但列只有 128 —— 放进来的长邮箱在 MySQL 上会撞列宽,报出来的是一个看不懂的 50000。
const (
	maxUserNameRunes = 64
	maxEmailLen      = 128
)

// normalizeEmail 校验并规整一个邮箱:去空白、转小写、必须是一个裸地址(不带显示名、不带空格)。
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", invalidf("邮箱不能为空")
	}
	if len(email) > maxEmailLen {
		return "", invalidf("邮箱过长(上限 %d 个字符)", maxEmailLen)
	}
	addr, err := mail.ParseAddress(email)
	// ParseAddress 接受 "名字 <a@b>" 这种带显示名的写法 —— 要求解析结果与输入一字不差,
	// 只认裸地址;再要求 @ 后面有带点的域名,挡住 "a@b" 这种内网里也不会有的写法。
	if err != nil || addr.Address != email || strings.ContainsAny(email, " \t") {
		return "", invalidf("邮箱格式不正确:%s", raw)
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || !strings.Contains(email[at+1:], ".") {
		return "", invalidf("邮箱格式不正确:%s", raw)
	}
	return email, nil
}

// checkUserName 校验姓名长度(空串由调用方决定是否回落到邮箱前缀)。
func checkUserName(name string) error {
	if utf8.RuneCountInString(name) > maxUserNameRunes {
		return invalidf("姓名过长(上限 %d 个字符)", maxUserNameRunes)
	}
	return nil
}

// checkRolesExist 要求每个角色 id 都存在,并说出是哪一个不存在。
func (s *Services) checkRolesExist(ids []int64) error {
	for _, id := range ids {
		if _, err := s.Repo.GetRole(id); err != nil {
			return invalidf("角色不存在:%d", id)
		}
	}
	return nil
}
