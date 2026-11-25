package service

import (
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
	"velagateway/pkg/crypto"
)

// 自助修改密码(黑盒 2026-10 工单 05 第 1 条)的拒绝理由。都包着 ErrBadRequest:
// 它们是"请求本身不成立",不是服务端故障。
var (
	ErrNoLocalPassword   = apimsg.Errorf("%w: 该账号没有本地口令", ErrBadRequest)
	ErrOldPasswordWrong  = apimsg.Errorf("%w: 原密码错误", ErrBadRequest)
	// 绑定 MFA 等敏感操作要求重新出示当前登录密码(L-AUTH4)
	ErrCurrentPasswordRequired = apimsg.Errorf("%w: 请输入当前登录密码", ErrBadRequest)
	ErrCurrentPasswordWrong    = apimsg.Errorf("%w: 当前登录密码错误", ErrBadRequest)
	ErrPasswordTooShort  = apimsg.Errorf("%w: 新密码至少 8 位", ErrBadRequest)
	ErrPasswordUnchanged = apimsg.Errorf("%w: 新密码不能与原密码相同", ErrBadRequest)
)

// minPasswordLen 与 CreateUser / AdminSetPassword / 前端校验同一条规则。
const minPasswordLen = 8

// ChangeOwnPassword 是本人修改自己的登录口令:要求出示旧口令,新口令规则与建号 / 重置一致。
//
// 成功后提升会话代次 —— 这个人在别处开着的会话(包括被盗的 token)全部作废,与管理员重置
// 口令同理(M1)。发起修改的这一个会话**不**要求重新登录:当场重签一枚新代次的 token 返回,
// 前端换上即可。人刚刚证明了自己知道旧口令,再把他踢回登录页换不来任何防护。
//
// 没有本地口令的账号(服务账号、只挂业务查询角色的业务方、口令为空的 SSO 账号)明确拒绝:
// 它们本来就不走口令登录,"改口令"等于凭空给它们造一把钥匙。
func (s *Services) ChangeOwnPassword(actor *model.User, oldPassword, newPassword string) (string, time.Time, error) {
	u, err := s.Repo.GetUserByID(actor.ID)
	if err != nil {
		return "", time.Time{}, ErrNotFound
	}
	u.ClientIP = actor.ClientIP
	u.SessionID = actor.SessionID
	if u.Kind == model.UserKindService || u.PasswordHash == "" || s.bizQueryOnly(u) {
		return "", time.Time{}, ErrNoLocalPassword
	}
	if !crypto.CheckPassword(u.PasswordHash, oldPassword) {
		// 失败也留痕:拿着别人会话的人反复猜旧口令,正是事后要从审计里读出来的事。
		s.auditAdminAction(u, "password.change.rejected 自助修改密码被拒:原密码错误")
		return "", time.Time{}, ErrOldPasswordWrong
	}
	if err := checkPasswordPolicy(newPassword); err != nil {
		return "", time.Time{}, err
	}
	if newPassword == oldPassword {
		return "", time.Time{}, ErrPasswordUnchanged
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := s.Repo.UpdateUserPassword(u.ID, hash); err != nil {
		return "", time.Time{}, err
	}
	if err := s.Repo.BumpTokenVersion(u.ID); err != nil {
		return "", time.Time{}, err
	}
	s.auditAdminAction(u, "password.change 自助修改登录口令(其它会话已作废)")

	// 重签当前会话:取回提升后的代次。不走 issueSession —— 那一条会记一行"login"审计,
	// 而这里并没有发生一次登录。
	fresh, err := s.Repo.GetUserByID(u.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	roleCode := ""
	if role, _ := s.Repo.GetRole(fresh.RoleID); role != nil {
		roleCode = role.Code
	}
	return s.JWT.IssueTTL(fresh.ID, fresh.RoleID, roleCode, fresh.Name, fresh.TokenVersion, s.SessionTTL())
}
