package service

// 存量账号与 SSO 的**绑定**。
//
// 绑定 = 确认"网关里这个账号,就是 SRD 里那个人"。SRD 没有可供读取的用户名单接口,所以
// 匹配发生在这个人**第一次经 SRD 授权登录**的那一刻:SRD 证明了他是这个邮箱的主人,邮箱又
// 对上了网关里的账号 —— 当场绑定(见 SSOLogin)。邮箱对不上的人经 SRD 进来会被
// 拒绝(ErrSSONoAccount),原来的账号不受影响,照常用口令,管理员对着账号列表处理。
//
// 绑定之后这个账号只能走 SSO 登录(管理员除外,保留口令作应急门,见 Login)。解绑由管理员
// 在【权限 · 用户】里点账号上的 SSO 标记完成,解绑后恢复口令登录;下次他再经 SRD 登录,
// 又会重新绑定。

import (
	"aegisdb/internal/model"
)

// SSOUnbind 解除一个账号的 SSO 绑定:它重新可以用口令登录。
func (s *Services) SSOUnbind(actor *model.User, userID int64) error {
	u, err := s.Repo.GetUserByID(userID)
	if err != nil {
		return ErrNotFound
	}
	if !u.SSOBound {
		return nil
	}
	if err := s.Repo.UpdateUserFields(userID, map[string]any{
		"sso_bound": false, "sso_user_name": "", "sso_bound_at": nil,
	}); err != nil {
		return err
	}
	s.auditAdminAction(actor, "sso.unbind 解除 SSO 绑定,恢复口令登录:"+u.Email)
	return nil
}
