package service

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"

	"aegisdb/internal/dto"
	"aegisdb/internal/model"
)

// 管理后台新建角色(2026-10-22)。
//
// 从前权限页上有一颗「新建角色」按钮,点开却是当前选中角色的编辑表单 —— 保存会改掉那个角色,
// 后端也从来没有建角色的接口。
//
// 两条规矩:
//   - **新角色什么都不给**:每个分层 × 每项能力写 deny、不勾任何菜单、不配实例标签。能力矩阵里
//     查不到行按 allow 算,不写 deny 的话一个刚建的空角色在所有分层上全放行。权限由管理员随后
//     在矩阵里逐项打开。
//   - **code 由服务端生成**(r_ + 随机串),不让人填:代码里不少地方按 code 认特殊角色(admin、
//     owner、audit、l2、bizquery…),让人填就可能填出一个"名字叫 admin"的新角色。

// CreateRole 建一个新角色,返回建好的角色。
func (s *Services) CreateRole(actor *model.User, req dto.RoleCreateReq) (*model.Role, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, invalidf("角色名称必填")
	}
	if len([]rune(name)) > 64 {
		return nil, invalidf("角色名称不能超过 64 个字")
	}
	layer := strings.TrimSpace(req.Layer)
	if layer == "" {
		layer = "自定义"
	}
	if len([]rune(layer)) > 32 {
		return nil, invalidf("层级说明不能超过 32 个字")
	}
	desc := strings.TrimSpace(req.Description)
	if len([]rune(desc)) > 255 {
		return nil, invalidf("描述不能超过 255 个字")
	}
	connRole := strings.TrimSpace(req.DefaultConnRole)
	if connRole == "" {
		connRole = "readonly"
	}
	if len([]rune(connRole)) > 64 {
		return nil, invalidf("默认连接身份不能超过 64 个字")
	}
	// 同名会让成员列表、审计里分不清是哪一个
	roles, err := s.Repo.ListRoles()
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		if strings.EqualFold(strings.TrimSpace(r.Name), name) {
			return nil, invalidf("已有同名角色「%s」", name)
		}
	}
	tiers, err := s.Repo.ListEnvTiers()
	if err != nil {
		return nil, err
	}
	codes := make([]string, 0, len(tiers))
	for _, t := range tiers {
		codes = append(codes, t.Code)
	}
	code, err := newRoleCode()
	if err != nil {
		return nil, err
	}
	role := &model.Role{
		Code: code, Name: name, Layer: layer, Description: desc,
		DefaultConnRole: connRole, CanApprove: req.CanApprove, Icon: "shield",
	}
	if err := s.Repo.CreateRoleDenyAll(role, codes, model.Capabilities); err != nil {
		return nil, err
	}
	s.AuditRoleChange(actor, role.ID, "create", map[string]any{
		"name": role.Name, "layer": role.Layer, "canApprove": role.CanApprove, "defaultConnRole": role.DefaultConnRole,
	})
	return role, nil
}

// LogUnscopedCustomRoles 在启动时列出没有标签的存量自定义角色:没有标签 = 不限实例,任何人加入就能碰
// 全部实例。新建的角色现在都带占位标签,存量的分不清是有意不限还是漏配,不替管理员决定,只提醒。
func (s *Services) LogUnscopedCustomRoles() {
	roles, err := s.Repo.CustomRolesWithoutTags()
	if err != nil {
		slog.Warn("自定义角色标签自检失败", "err", err)
		return
	}
	for _, r := range roles {
		slog.Warn("自定义角色没有设置实例标签,成员可访问全部实例:请在【权限】里确认,不需要就选上标签", "role", r.Name, "code", r.Code)
	}
}

func newRoleCode() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "r_" + hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------- 删除

// IsDeletableRole:只有经「新建角色」建的(code 以 r_ 开头)才能删。内置角色与系统角色(admin、owner、
// l2、ro、audit、bizquery…)代码里按 code 认,删掉它们会让对应的规则静默失效。
func IsDeletableRole(code string) bool { return strings.HasPrefix(code, "r_") }

// DeleteRole 删一个自定义角色。三种情况拒绝,并说清为什么:
//   - 不是自定义角色;
//   - 还有人挂着它(主角色或成员)—— 删掉会让人突然没了角色,要先把人移走;
//   - 还被流程引用(某个阶段的审批角色 / 确认角色),或有未结束的发布单停在等它确认的闸上 ——
//     删了那些闸就再也过不去。
func (s *Services) DeleteRole(actor *model.User, id int64) error {
	role, err := s.Repo.GetRole(id)
	if err != nil {
		return ErrNotFound
	}
	if !IsDeletableRole(role.Code) {
		return invalidf("「%s」是内置或系统角色,不能删除", role.Name)
	}
	n, err := s.Repo.CountRoleUsers(id)
	if err != nil {
		return err
	}
	if n > 0 {
		return invalidf("角色「%s」下还有 %d 个用户,请先把他们移出这个角色", role.Name, n)
	}
	pipeStages, liveStages, err := s.Repo.StageConfigsForRoleCheck()
	if err != nil {
		return err
	}
	refs := func(cfg string) bool { return cfgApproverRole(cfg) == role.Code || cfgConfirmRole(cfg) == role.Code }
	var pipes []string
	seen := map[int64]bool{}
	for _, st := range pipeStages {
		if refs(st.Config) && !seen[st.PipelineID] {
			seen[st.PipelineID] = true
			name := "#" + strconv.FormatInt(st.PipelineID, 10)
			if p, perr := s.Repo.GetPipeline(st.PipelineID); perr == nil {
				name = p.Name
			}
			pipes = append(pipes, name)
		}
	}
	if len(pipes) > 0 {
		return invalidf("角色「%s」还被发布流程用作审批 / 确认角色:%s。请先在流程里换掉它", role.Name, strings.Join(pipes, "、"))
	}
	for _, st := range liveStages {
		if refs(st.Config) {
			return invalidf("角色「%s」还有未结束的发布单在等它审批 / 确认,请等这些发布单结束后再删", role.Name)
		}
	}
	if err := s.Repo.DeleteRoleCascade(id); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.role.delete role="+role.Code+" name="+role.Name)
	return nil
}
