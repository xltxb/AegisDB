package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/model"
	"aegisdb/internal/review"
	"aegisdb/pkg/crypto"
)

// 开放接口 —— 外部系统提交 SQL 升级单。
//
// The whole feature is one idea: an external system (a DevOps platform, a CI
// job, a change-management tool) can raise a release ticket, and from that point
// the ticket is governed by exactly the same machinery a console release is —
// the same flow templates, the same review rules, the same approval chain, the
// same execute-time re-judgement, the same audit chain. The open API is a DOOR,
// not a second pipeline; anything else would make it the bypass everyone would
// eventually use.
//
// What the door adds over the console path is only what a machine caller needs:
// a non-interactive credential, name-based addressing (an external system knows
// "order-cluster", not connection id 12), and idempotency (it will retry).

// ---------------------------------------------------------------- 服务账号

// CreateServiceAccount mints a machine principal for external systems (升级单
// 平台、CI/CD) to act through.
//
// It is a User row with kind=service, and deliberately NOTHING else special:
// roles decide what its releases may touch, tags narrow which instances it
// sees, the audit chain attributes to it by name — every judgement layer
// treats it exactly like a person. The two differences both point the strict
// way: it can never log into the console (no password exists and none can be
// set), and the MFA mandate does not apply to it (a machine cannot enroll
// TOTP; its second factor is the API credential's bcrypt secret plus that
// credential's own IP allowlist).
func (s *Services) CreateServiceAccount(actor *model.User, req dto.ServiceAccountReq) (*model.User, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, apimsg.Errorf("服务账号名称不能为空")
	}
	if len(req.RoleIDs) == 0 {
		return nil, apimsg.Errorf("请为服务账号指定至少一个角色")
	}
	if err := s.validateRoleIDs(req.RoleIDs); err != nil {
		return nil, err
	}
	// 服务账号生来就是给凭据用的:带着 admin 出生,等于签一把凭据就得到一个不经登录、
	// 不经 MFA 的管理员(.scratch/blackbox-fixes 03)。
	if s.roleIDsIncludeAdmin(req.RoleIDs) {
		return nil, apimsg.Errorf("服务账号不能带平台管理员角色:它的唯一入口是 API 凭据,带上 admin 就成了一串密钥即可用的管理员")
	}
	email := serviceAccountEmail(name)
	if _, err := s.Repo.GetUserByEmail(email); err == nil {
		return nil, apimsg.Errorf("同名服务账号已存在: %s", name)
	}
	dept := strings.TrimSpace(req.Dept)
	if dept == "" {
		dept = "服务账号"
	}
	u := &model.User{
		Name: clip(name, 60), Email: email, RoleID: req.RoleIDs[0],
		Status: "active", Kind: model.UserKindService,
		// PasswordHash stays EMPTY. Login fail-closes on an empty hash AND on
		// kind=service — two locks on a door this account must never use.
		Dept: clip(dept, 60), Initials: "SA", LastActive: "—",
	}
	if err := s.Repo.CreateUser(u); err != nil {
		return nil, err
	}
	if err := s.Repo.SetUserRoles(u.ID, req.RoleIDs); err != nil {
		return nil, err
	}
	if len(req.Tags) > 0 {
		if err := s.Repo.SetUserTags(u.ID, req.Tags); err != nil {
			return nil, err
		}
	}
	s.auditAdminAction(actor, "admin.serviceaccount.create name="+u.Name)
	return u, nil
}

// ListServiceAccounts returns the machine principals with what the credential
// binding UI needs: roles, tags, and how many credentials already act as each.
func (s *Services) ListServiceAccounts() []dto.ServiceAccountView {
	users, err := s.Repo.ListUsers()
	if err != nil {
		return []dto.ServiceAccountView{}
	}
	clients, _ := s.Repo.ListAPIClients()
	bound := map[int64]int{}
	for _, c := range clients {
		bound[c.UserID]++
	}
	out := []dto.ServiceAccountView{}
	for _, u := range users {
		if u.Kind != model.UserKindService {
			continue
		}
		roles, _ := s.Repo.RolesOfUser(u.ID)
		tags, _ := s.Repo.TagsForUser(u.ID)
		if tags == nil {
			tags = []string{}
		}
		if roles == nil {
			roles = []string{}
		}
		out = append(out, dto.ServiceAccountView{
			ID: u.ID, Name: u.Name, Email: u.Email, Status: u.Status, Dept: u.Dept,
			Roles: roles, Tags: tags, Clients: bound[u.ID],
		})
	}
	return out
}

// serviceAccountEmail derives the account's unique identity from its name. It
// LOOKS like an email because tbl_user's identity column is one; nothing is
// ever delivered to it. ASCII survives; anything else (中文名) hashes into the
// slug so two different Chinese names never collide on "svc-@…".
func serviceAccountEmail(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8]
	if slug == "" {
		return "svc-" + sum + "@service.aegisdb"
	}
	return "svc-" + clip(slug, 40) + "-" + sum + "@service.aegisdb"
}

// ---------------------------------------------------------------- 凭据管理

// CreateAPIClient issues a credential and returns it together with the ONLY
// plaintext copy of its secret.
//
// The service account is not optional and not defaulted: it is what the
// capability matrix, the tag scope and the audit trail key off. Binding a client
// to an administrator "so it just works" would hand every integration the right
// to change any instance, which is the failure this parameter exists to make
// visible at creation time.
func (s *Services) CreateAPIClient(actor *model.User, req dto.APIClientReq) (*model.APIClient, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, "", apimsg.Errorf("凭据名称不能为空")
	}
	svcUser, err := s.Repo.GetUserByID(req.UserID)
	if err != nil {
		return nil, "", apimsg.Errorf("服务账号不存在")
	}
	if err := s.requireBindableAccount(svcUser); err != nil {
		return nil, "", err
	}
	// 没勾任何范围(省略或 [])= 默认集合,这是对接文档写明的语义;勾了就逐个认,
	// 认不出的整单拒绝。
	scopes := model.AllScopes
	if len(req.Scopes) > 0 {
		if scopes, err = validateScopes(req.Scopes); err != nil {
			return nil, "", err
		}
	}
	allowIPs := ""
	if req.AllowIPs != nil {
		if allowIPs, err = validateAllowIPs(*req.AllowIPs); err != nil {
			return nil, "", err
		}
	}
	key, secret, err := newAPICredential()
	if err != nil {
		return nil, "", err
	}
	hash, err := crypto.HashPassword(secret)
	if err != nil {
		return nil, "", err
	}
	pipelineID := int64(0)
	if req.PipelineID != nil && *req.PipelineID > 0 {
		if err := s.validateClientPipeline(*req.PipelineID); err != nil {
			return nil, "", err
		}
		pipelineID = *req.PipelineID
	}
	cl := &model.APIClient{
		Name: clip(name, 60), Key: key, SecretHash: hash,
		UserID: svcUser.ID, UserName: svcUser.Name,
		AllowIPs: allowIPs, Scopes: strings.Join(scopes, ","),
		PipelineID: pipelineID,
		Enabled: true, CreatedBy: actor.ID,
	}
	if err := s.Repo.CreateAPIClient(cl); err != nil {
		return nil, "", err
	}
	// 只记公开半段的 key,secret 明文此刻只存在于返回值里。
	s.auditAdminAction(actor, fmt.Sprintf("admin.apiclient.create 签发凭据「%s」key=%s 绑定账号 %s scopes=%s allowIps=%s pipelineId=%d",
		cl.Name, cl.Key, cl.UserName, cl.Scopes, orDash(cl.AllowIPs), cl.PipelineID))
	// The full token is what the caller puts in `Authorization: Bearer`. It is
	// assembled here and never stored: only the bcrypt hash of the secret half is.
	return cl, key + "." + secret, nil
}

// ListAPIClients returns the credentials (never a secret — there is no copy to
// return, only a hash).
func (s *Services) ListAPIClients() []model.APIClient {
	cs, err := s.Repo.ListAPIClients()
	if err != nil {
		return []model.APIClient{}
	}
	return cs
}

// UpdateAPIClient edits what an operator owns: the name, the IP allowlist, the
// scopes and the enabled flag. The key and the secret are immutable — rotating a
// credential means issuing a new one and deleting the old, so the two never
// silently swap under an integration that is still using it.
func (s *Services) UpdateAPIClient(actor *model.User, id int64, req dto.APIClientReq) (*model.APIClient, error) {
	cl, err := s.Repo.GetAPIClient(id)
	if err != nil {
		return nil, ErrNotFound
	}
	// Partial-update semantics: only what the request SAID changes. The enable
	// switch sends {enabled} alone, and the fields it stays silent about are
	// security controls — an update that zeroed the absent ones once turned
	// "停用凭据" into "顺手清空它的 IP 白名单".
	fields := map[string]any{}
	if req.Enabled != nil {
		// 重新启用要复核绑定账号是不是管理员(2026-11 审计 V-06)。GuardAdminGrant 只挡启用中的凭据,
		// 于是"停用 → 加 admin → 启用"就造出一把不经登录、不经 MFA 的管理员凭据。只查管理员,不复跑
		// requireBindableAccount:绑在真人名下的存量凭据不追溯,停用后还要能启用回来。这次同时改绑时
		// 新账号由下面那道闸判。
		if *req.Enabled && !cl.Enabled && (req.UserID <= 0 || req.UserID == cl.UserID) {
			if bound, uerr := s.Repo.GetUserByID(cl.UserID); uerr == nil && s.roleIDsIncludeAdmin(s.Repo.EffectiveRoleIDs(bound)) {
				return nil, apimsg.Errorf("凭据绑定的账号 %s 已是平台管理员,不能启用:那会让一串密钥拥有管理员权限,且不经登录与 MFA。请改绑权限更小的服务账号,或先收回管理员角色", bound.Name)
			}
		}
		fields["enabled"] = *req.Enabled
	}
	if req.AllowIPs != nil {
		ips, verr := validateAllowIPs(*req.AllowIPs)
		if verr != nil {
			return nil, verr
		}
		fields["allow_ips"] = ips
	}
	if req.PipelineID != nil {
		if *req.PipelineID > 0 {
			if err := s.validateClientPipeline(*req.PipelineID); err != nil {
				return nil, err
			}
		}
		fields["pipeline_id"] = *req.PipelineID // 0 = 解绑,回到分层默认流程
	}
	if n := strings.TrimSpace(req.Name); n != "" {
		fields["name"] = clip(n, 60)
	}
	// nil = 不改;[] 不是"不改"也不是"清空"(凭据不能没有范围)—— 明确报错,
	// 而不是像从前那样静默保持原值,让调用方以为自己清掉了。
	if req.Scopes != nil {
		if len(req.Scopes) == 0 {
			return nil, apimsg.Errorf("scopes 不能为空数组:凭据至少要有一个权限范围;不修改请省略该字段")
		}
		scopes, verr := validateScopes(req.Scopes)
		if verr != nil {
			return nil, verr
		}
		fields["scopes"] = strings.Join(scopes, ",")
	}
	if req.UserID > 0 && req.UserID != cl.UserID {
		u, uerr := s.Repo.GetUserByID(req.UserID)
		if uerr != nil {
			return nil, apimsg.Errorf("账号不存在")
		}
		// 换绑走的是同一道闸。只在创建时挡、改绑时放过去,等于没挡。
		if err := s.requireBindableAccount(u); err != nil {
			return nil, err
		}
		fields["user_id"], fields["user_name"] = u.ID, u.Name
	}
	if err := s.Repo.UpdateAPIClientFields(id, fields); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		detail := describeClientFields(fields)
		// IP 白名单是这把凭据的来源闸,改它要能事后对出"从什么改成了什么"——
		// 只记新值的话,一次被清空(= 不限来源)的修改在审计里看不出原来挡着谁。
		if _, ok := fields["allow_ips"]; ok {
			detail += " allow_ips_before=" + orDash(cl.AllowIPs)
		}
		// 权限范围同理:加上一项 release:approve 和去掉一项,只看新值分不出来。
		if _, ok := fields["scopes"]; ok {
			detail += " scopes_before=" + orDash(cl.Scopes)
		}
		s.auditAdminAction(actor, fmt.Sprintf("admin.apiclient.update 修改凭据「%s」key=%s %s",
			cl.Name, cl.Key, detail))
	}
	return s.Repo.GetAPIClient(id)
}

func (s *Services) DeleteAPIClient(actor *model.User, id int64) error {
	cl, err := s.Repo.GetAPIClient(id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.Repo.DeleteAPIClient(id); err != nil {
		return err
	}
	s.auditAdminAction(actor, fmt.Sprintf("admin.apiclient.delete 吊销凭据「%s」key=%s 绑定账号 %s", cl.Name, cl.Key, cl.UserName))
	return nil
}

// describeClientFields 把一次凭据更新写成稳定的 k=v 列表(按列名排序),供审计。
// 只有这些列会出现在 fields 里,其中没有任何密钥材料。
func describeClientFields(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := fmt.Sprint(fields[k])
		if v == "" {
			v = "—"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

// validateScopes 逐个认范围:认不出的整单拒绝(40001),重复的去掉。
//
// 从前是静默丢弃认不出的、全丢光了再回落默认集合 —— 把 "release:write" 拼错的调用方
// 拿到的是一把他没要过的默认凭据,控制台上看起来还像成功了。
func validateScopes(in []string) ([]string, error) {
	known := map[string]bool{}
	for _, s := range model.KnownScopes {
		known[s] = true
	}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if !known[s] {
			return nil, apimsg.Errorf("未知的权限范围 %q,可选值: %s", s, strings.Join(model.KnownScopes, " / "))
		}
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// validateAllowIPs 逐项校验凭据 IP 白名单(IP 或 CIDR,逗号/空白/换行分隔,与
// middleware.apiIPAllowed 的切分一致),返回规范化后的逗号串;空串 = 不限来源。
// 重复项按解析后的地址/网段比("::1" 与 "0:0::1"、"10.0.0.5/24" 与 "10.0.0.0/24"
// 在鉴权时匹配的是同一批来源),只留第一次出现的写法。
//
// 不校验的后果是:写错的一项在鉴权时永远匹配不上,管理员以为加了白,调用方被 40301
// 挡在门外,两边都看不出原因。
func validateAllowIPs(raw string) (string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for i, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		canon := ""
		if strings.Contains(f, "/") {
			if _, n, err := net.ParseCIDR(f); err == nil {
				canon = n.String()
			}
		} else if ip := net.ParseIP(f); ip != nil {
			canon = ip.String()
		}
		if canon == "" {
			return "", apimsg.Errorf("IP 白名单第 %d 项 %q 不是合法的 IP 或 CIDR", i+1, f)
		}
		if seen[canon] {
			continue
		}
		seen[canon] = true
		out = append(out, f)
	}
	joined := strings.Join(out, ",")
	if len(joined) > 512 {
		return "", apimsg.Errorf("IP 白名单过长(上限 512 字符),请合并为网段")
	}
	return joined, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// newAPICredential mints a public key and a secret from crypto/rand.
func newAPICredential() (key, secret string, err error) {
	kb := make([]byte, 8)
	if _, err = rand.Read(kb); err != nil {
		return "", "", err
	}
	sb := make([]byte, 24)
	if _, err = rand.Read(sb); err != nil {
		return "", "", err
	}
	// The key carries no dots: the token splits on the FIRST dot (see
	// middleware.apiCredential), so a dot in the key half would truncate it.
	return "ak_" + hex.EncodeToString(kb), base64.RawURLEncoding.EncodeToString(sb), nil
}

// ---------------------------------------------------------------- 提交升级单

// CreateReleaseFromAPI is the open API's entry point: resolve what the caller
// named, materialise a script if they sent one, then hand the whole thing to the
// SAME submit path the console uses.
func (s *Services) CreateReleaseFromAPI(u *model.User, cl *model.APIClient, req dto.OpenReleaseReq) (*model.Release, error) {
	if cl == nil || u == nil {
		return nil, ErrForbidden
	}
	// Idempotency first: a retry must find the ticket its predecessor created,
	// before this call does any of the work that would create a second one.
	var idem *string
	if ref := strings.TrimSpace(req.ExternalRef); ref != "" {
		k := fmt.Sprintf("%d:%s", cl.ID, clip(ref, 120))
		idem = &k
		if prev, err := s.Repo.GetReleaseByIdemKey(k); err == nil {
			return prev, nil
		}
	}

	conn, err := s.resolveConnection(req.ConnectionID, req.Instance)
	if err != nil {
		return nil, err
	}
	// 发布流程是网关侧策略:凭据绑定的流水线 > 目标分层的默认流程。请求里出现
	// pipeline 字段一律拒绝而非忽略 —— 静默忽略会让调用方以为自己指定成功了。
	if req.PipelineID > 0 || strings.TrimSpace(req.Pipeline) != "" {
		return nil, apimsg.Errorf("发布流程由网关侧配置(凭据绑定或分层默认),请移除 pipeline/pipelineId 字段;如需调整流程请联系管理员")
	}
	pipelineID := cl.PipelineID

	inner := dto.ReleaseReq{
		Title: strings.TrimSpace(req.Title), PipelineID: pipelineID,
		ConnectionID: conn.ID, Database: strings.TrimSpace(req.Database),
		Reason: strings.TrimSpace(req.Reason), MfaCode: req.MfaCode,
		ChangeType: req.ChangeType,
	}
	if inner.Title == "" {
		inner.Title = "外部升级单 " + strings.TrimSpace(req.ExternalRef)
	}

	// A script and inline SQL are the same thing at different sizes, and the
	// difference in handling is deliberate: a script is written to disk, hashed,
	// and referenced, so what executes later is verified to be the bytes that
	// were reviewed. Inline SQL lives in the row.
	script, filename, serr := scriptFromRequest(req)
	if serr != nil {
		return nil, serr
	}
	switch {
	case script != "":
		up, uerr := s.storeScript(u, filename, script, "api")
		if uerr != nil {
			if uerr == ErrScriptPathUnset {
				return nil, ErrScriptPathUnset
			}
			return nil, apimsg.Errorf("脚本落盘失败: %w", uerr)
		}
		inner.ScriptUploadID = up.ID
	case strings.TrimSpace(req.SQL) != "":
		inner.SQL = req.SQL
	default:
		return nil, apimsg.Errorf("请提供 sql 或 script 内容")
	}

	return s.submitRelease(u, inner, releaseOrigin{
		Source: model.ReleaseSourceAPI, ClientID: cl.ID, ClientName: cl.Name,
		ExternalRef: strings.TrimSpace(req.ExternalRef), IdemKey: idem,
	})
}

// scriptFromRequest returns the script body an external caller sent, whichever
// way they sent it. base64 exists because a CI system embedding a migration in
// JSON has to escape it otherwise, and an escaping bug in someone else's YAML
// template must not become a mangled statement here.
func scriptFromRequest(req dto.OpenReleaseReq) (content, filename string, err error) {
	filename = strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = "upgrade.sql"
	}
	if b := strings.TrimSpace(req.ScriptBase64); b != "" {
		raw, derr := base64.StdEncoding.DecodeString(b)
		if derr != nil {
			// Tolerate the URL-safe/unpadded variants rather than failing a caller
			// whose language's base64 default differs from ours.
			if raw, derr = base64.RawStdEncoding.DecodeString(b); derr != nil {
				if raw, derr = base64.URLEncoding.DecodeString(b); derr != nil {
					return "", "", apimsg.Errorf("scriptBase64 不是合法的 base64")
				}
			}
		}
		if len(raw) > MaxOpenScriptBytes {
			return "", "", errOpenScriptTooLarge
		}
		return string(raw), filename, nil
	}
	if strings.TrimSpace(req.Script) != "" {
		if len(req.Script) > MaxOpenScriptBytes {
			return "", "", errOpenScriptTooLarge
		}
		return req.Script, filename, nil
	}
	return "", filename, nil
}

// MaxOpenScriptBytes 是开放接口一份脚本的上限(15MB)。multipart 的 file、JSON 的
// script、解码后的 scriptBase64 三条通道同一尺寸、同一句拒绝 —— 从前 JSON 通道不设限,
// 一份 100MB 的脚本会一路落盘、进审查。
const MaxOpenScriptBytes = 15 << 20

var errOpenScriptTooLarge = apimsg.Errorf("脚本超过 15MB 上限")

// resolveConnection accepts an id or a NAME, because an external system knows
// the instance it deploys to by name, not by this gateway's row id. Both the
// bare name ("order-cluster") and the console's display form ("prod-order-cluster")
// resolve; an ambiguous or unknown name is an error rather than a guess.
func (s *Services) resolveConnection(id int64, name string) (*model.Connection, error) {
	if id > 0 {
		conn, err := s.Repo.GetConnection(id)
		if err != nil {
			return nil, apimsg.Errorf("实例不存在: %d", id)
		}
		return conn, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apimsg.Errorf("请提供 instance(实例名)或 connectionId")
	}
	conns, err := s.Repo.ListConnections()
	if err != nil {
		return nil, err
	}
	var hits []model.Connection
	for _, c := range conns {
		if strings.EqualFold(c.Name, name) || strings.EqualFold(c.Env+"-"+c.Name, name) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		return nil, apimsg.Errorf("实例不存在: %s", name)
	}
	if len(hits) > 1 {
		// Instance names are unique per gateway, but the env-prefixed form could
		// in principle match two rows; refuse rather than pick one, because the
		// wrong pick here is a change applied to the wrong database.
		return nil, apimsg.Errorf("实例名 %s 不唯一,请改用 connectionId", name)
	}
	return &hits[0], nil
}

// validateClientPipeline checks a to-be-bound flow exists and is enabled — a
// credential bound to a dead template would refuse every ticket it raises.
func (s *Services) validateClientPipeline(id int64) error {
	p, err := s.Repo.GetPipeline(id)
	if err != nil {
		return apimsg.Errorf("发布流程不存在: %d", id)
	}
	if !p.Enabled {
		return apimsg.Errorf("发布流程「%s」已停用,不能绑定", p.Name)
	}
	return nil
}

// ---------------------------------------------------------------- 查询

// ReleaseStatusForClient returns one ticket by its number, scoped to the client
// that raised it.
//
// A credential can read the releases ITS OWN credential created, not everything
// on the gateway: an integration that can see every production change is an
// information leak that no integrator asked for. 这里按**凭据**而不是按服务账号划界
// (与 openapi 契约一致):同一个服务账号常常借给好几套系统各发一把凭据,按账号划界
// 等于让它们互相看单。从前还放行了"服务账号本人建的单"与监督角色,两条都已收回。
//
// 2026-10-26 回归 02:轮换凭据的做法是"签新凭据、删旧凭据",只认同一把凭据时新凭据
// 轮询旧凭据建的在途单变成 40300。决定:**同一服务账号**名下的凭据可互查对方经开放接口
// 建的单(rel.CreatorID == 凭据绑定的账号且 rel.ClientID != 0);不同服务账号仍隔离,
// 控制台建的单(ClientID == 0)与监督角色照旧不放行。终止(Abort)仍只认同一把凭据。
func (s *Services) ReleaseStatusForClient(u *model.User, cl *model.APIClient, relNo string) (*dto.ReleaseView, error) {
	rel, err := s.Repo.GetReleaseByNo(strings.TrimSpace(relNo))
	if err != nil {
		return nil, ErrNotFound
	}
	if cl == nil {
		return nil, ErrForbidden
	}
	sameClient := rel.ClientID == cl.ID
	sameAccount := u != nil && rel.ClientID != 0 && rel.CreatorID == u.ID && cl.UserID == u.ID
	if !sameClient && !sameAccount {
		return nil, ErrForbidden
	}
	v := s.releaseView(*rel)
	return &v, nil
}

// AbortReleaseForClient lets the system that raised a ticket cancel it (a
// pipeline run that was superseded, a build that was rolled back). The same
// "not while it is executing" rule applies as in the console.
func (s *Services) AbortReleaseForClient(u *model.User, cl *model.APIClient, relNo string) error {
	rel, err := s.Repo.GetReleaseByNo(strings.TrimSpace(relNo))
	if err != nil {
		return ErrNotFound
	}
	if cl == nil || rel.ClientID != cl.ID {
		return ErrForbidden
	}
	// 终止是一次状态变更,控制台那条路由会留痕,开放接口这条也要:actor = 服务账号,
	// operator = API:<凭据名>。被拒的(状态不对、已被别人决定)同样记一笔,结果写 rejected。
	var conn *model.Connection
	if c, cerr := s.Repo.GetConnection(rel.ConnectionID); cerr == nil {
		conn = c
	}
	if err := s.AbortRelease(u, rel.ID); err != nil {
		s.recordAuditFrom(model.AuditSourceRelease, u, apiOperator(cl), conn, fmt.Sprintf("open.release.abort 终止升级单 %s「%s」被拒:%s",
			rel.RelNo, rel.Title, err.Error()), model.RiskLow, model.ResultRejected, "", "")
		return err
	}
	s.recordAuditFrom(model.AuditSourceRelease, u, apiOperator(cl), conn, fmt.Sprintf("open.release.abort 终止升级单 %s「%s」",
		rel.RelNo, rel.Title), model.RiskLow, model.ResultExecuted, "", "")
	return nil
}

// OpenReleaseResp renders a release in the external contract shape. withLog is
// false on the create response (there is nothing to report yet) and true on a
// status poll, which is where a caller looks to find out WHY a stage failed.
func (s *Services) OpenReleaseResp(rel *model.Release, withLog bool) dto.OpenReleaseResp {
	v := s.releaseView(*rel) // redacts credentials on the way out
	out := dto.OpenReleaseResp{
		RelNo: v.RelNo, Title: v.Title, Status: v.Status, Risk: v.Risk,
		ChangeType: v.ChangeType,
		Instance: v.Instance, Database: v.Database, Env: v.Env, Pipeline: v.PipelineName,
		ExternalRef: v.ExternalRef, Error: v.Error,
		CreatedAt: v.CreatedAt.Format(time.RFC3339),
		Stages:    make([]dto.OpenStage, 0, len(v.Stages)),
	}
	if v.FinishedAt != nil {
		out.FinishedAt = v.FinishedAt.Format(time.RFC3339)
	}
	out.Precheck = openPrecheckOf(rel.Precheck)
	for _, st := range v.Stages {
		one := dto.OpenStage{
			Order: st.StepOrder, Name: st.Name, Type: st.Type,
			Status: st.Status, ApprovalNo: st.ApprovalNo,
		}
		if withLog {
			one.Log = st.Log
		}
		if st.Type == model.StageApprove && st.ApprovalID > 0 {
			one.Approvers = s.openApprovers(st.ApprovalID)
		}
		if st.Type == model.StageManual && st.Status == model.RunWaiting {
			one.Approvers = s.openGateConfirmers(rel, &st)
		}
		if st.StartedAt != nil {
			one.StartedAt = st.StartedAt.Format(time.RFC3339)
		}
		if st.FinishedAt != nil {
			one.FinishedAt = st.FinishedAt.Format(time.RFC3339)
		}
		out.Stages = append(out.Stages, one)
	}
	return out
}

// CheckSQLForClient is CheckSQL addressed the way an external caller addresses
// things: by instance name, with the body arriving as SQL, script or base64.
//
// 每次调用都进审计(operator = API:<凭据名>),只记结论不记 SQL 原文 —— 审查的是还没
// 合并的代码,结论才是事后要追的东西;被拒的调用同样记一笔。
func (s *Services) CheckSQLForClient(u *model.User, cl *model.APIClient, req dto.OpenReviewReq) (*dto.ReviewCheckResp, error) {
	res, err := s.checkSQLForClient(u, req)
	instance := strings.TrimSpace(req.Instance)
	if instance == "" && req.ConnectionID > 0 {
		instance = fmt.Sprintf("#%d", req.ConnectionID)
	}
	var conn *model.Connection
	if res != nil && res.Instance != "" {
		instance = res.Instance
		if c, cerr := s.resolveConnection(req.ConnectionID, req.Instance); cerr == nil {
			conn = c
		}
	}
	if err != nil {
		s.recordAuditBy(u, apiOperator(cl), conn, fmt.Sprintf("open.sqlreview 规范审查预检被拒 · 实例 %s · %s",
			orDash(instance), clip(err.Error(), 200)), model.RiskLow, model.ResultRejected, "", "")
		return nil, err
	}
	r := res.Result
	s.recordAuditBy(u, apiOperator(cl), conn, fmt.Sprintf("open.sqlreview 规范审查预检 · 实例 %s · 方言 %s · %d 条语句 · 错误 %d / 警告 %d / 提示 %d · passed=%t",
		orDash(instance), r.Dialect, r.Statements, r.Errors, r.Warnings, r.Infos, r.Passed), model.RiskLow, model.ResultExecuted, "", "")
	return res, nil
}

// validOpenDialect 校验 /open/sql-review 的 dialect 可选值(与 openapi 契约的 enum 一致)。
// 认不出的值从前一路落到 generic,只跑通用规则,于是返回一个误导人的 passed:true。
//
// 5961d26 收紧成只认四个方言码之后,外部 CI 一直在传的 all / generic、引擎名(gaussdb、polardb、
// postgres……)都变成 40001(2026-10-26 回归排查)。这些值照旧认:all / generic 与没有专属规则的
// 引擎(PostgreSQL、SQLite)按 generic 跑通用规则,带方言词的引擎名按 review.DialectFor 映射。
// 仍拒绝的只有认不出来的拼写错误 —— 那正是收紧要防的"误导人的 passed:true"。
func validOpenDialect(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	if d == "" {
		return "", nil
	}
	for _, k := range review.Dialects {
		if d == k {
			return d, nil
		}
	}
	switch d {
	case review.DialectAll, review.DialectGeneric, "postgres", "postgresql", "pg", "sqlite":
		return review.DialectGeneric, nil
	case "aurora":
		// Aurora 是 MySQL 兼容引擎,引擎名里没有方言词,DialectFor 认不出(2026-10-26 回归 19)。
		// 只在开放接口这里映射,不动共享的 DialectFor,免得改变按实例引擎选方言的既有结果。
		return review.DialectMySQL, nil
	}
	if m := review.DialectFor(d); m != review.DialectGeneric {
		return m, nil
	}
	return "", apimsg.Errorf("dialect 取值无效: %q,可选值: %s", d, strings.Join(review.Dialects, " / "))
}

func (s *Services) checkSQLForClient(u *model.User, req dto.OpenReviewReq) (*dto.ReviewCheckResp, error) {
	// 带了实例时以实例引擎为准(CheckSQL),dialect 不参与,也就不该因为它报错 —— 从前校验跑在
	// 解析实例之前,带 instance 又带了旧 dialect 值的调用一样 40001。
	if req.ConnectionID == 0 && strings.TrimSpace(req.Instance) == "" {
		dialect, derr := validOpenDialect(req.Dialect)
		if derr != nil {
			return nil, derr
		}
		req.Dialect = dialect
	}
	sql := req.SQL
	if body, _, err := scriptFromRequest(dto.OpenReleaseReq{
		Script: req.Script, ScriptBase64: req.ScriptBase64, Filename: req.Filename,
	}); err != nil {
		return nil, err
	} else if body != "" {
		sql = body
	}
	if strings.TrimSpace(sql) == "" {
		return nil, apimsg.Errorf("请提供 sql 或 script 内容")
	}
	connID := req.ConnectionID
	if connID == 0 && strings.TrimSpace(req.Instance) != "" {
		conn, err := s.resolveConnection(0, req.Instance)
		if err != nil {
			return nil, err
		}
		connID = conn.ID
	}
	return s.CheckSQL(u, connID, req.Dialect, sql, req.ChangeType)
}

// APIClientLastUsed is a display helper for the console listing.
func APIClientLastUsed(c model.APIClient) string {
	if c.LastUsedAt == nil {
		return ""
	}
	return c.LastUsedAt.Format(time.RFC3339)
}

// requireBindableAccount refuses the principals an API credential must not act as.
//
// 2026-10 起(.scratch/blackbox-fixes 03):**新建/改绑凭据只能绑服务账号**,且不能是
// 平台管理员。
//
// 早先(白盒审计 M-5)的判断是"绑真人是产品有意提供的用法,只挡管理员"。黑盒测试证明
// 只在签发时挡管理员不够:真人账号随时可能被加上 admin,一把挂在他名下的凭据就跟着成了
// 不经登录、不经 MFA 的管理员身份。人走登录、机器走凭据 —— 把这条边界落到绑定上。
//
// **存量不追溯**:升级前已经绑在真人名下的凭据,鉴权时照常放行(生产里可能有在用的
// 集成,追溯会让它们突然断掉),由管理员在凭据页自行改绑或吊销。追溯之外的那条口子
// 由 GuardAdminGrant 堵:被启用凭据绑定的账号,加不上 admin。
func (s *Services) requireBindableAccount(u *model.User) error {
	if u == nil {
		return apimsg.Errorf("账号不存在")
	}
	if u.Kind != model.UserKindService {
		return apimsg.Errorf("API 凭据只能绑定服务账号,%s 是人类账号:请先在「开放接口」里创建服务账号再绑定", u.Name)
	}
	if u.Status != "active" {
		return apimsg.Errorf("账号 %s 未启用", u.Name)
	}
	if s.roleIDsIncludeAdmin(s.Repo.EffectiveRoleIDs(u)) {
		return apimsg.Errorf("不能把 API 凭据绑到平台管理员 %s:那会让一串密钥拥有管理员权限,且不经登录与 MFA。请改用权限更小的服务账号", u.Name)
	}
	return nil
}

// roleIDsIncludeAdmin 判断一组角色 id 里有没有平台管理员。
func (s *Services) roleIDsIncludeAdmin(ids []int64) bool {
	for _, code := range s.Repo.RoleCodesForIDs(ids) {
		if code == "admin" {
			return true
		}
	}
	return false
}

// AdminGrantRefusal 是 GuardAdminGrant 的拒绝:理由要原样透给管理员("先停用/改绑
// 凭据"),一句"更新失败"会让人去修错的东西。
type AdminGrantRefusal struct{ err error }

func (e *AdminGrantRefusal) Error() string { return e.err.Error() }

// MsgRef 让界面翻译这句拒绝理由(apimsg)。
func (e *AdminGrantRefusal) MsgRef() *model.RuleRef { return apimsg.RefOf(e.err) }

// GuardAdminGrant 在一次角色变更把账号的角色集合改成 next 之前检查:这次变更是否
// **新加上**平台管理员,而该账号是服务账号或正被启用的 API 凭据绑定。
//
// 这是凭据不追溯之后剩下的那条口子(.scratch/blackbox-fixes 03):签发时挡住了管理员,
// 签发后再给账号加 admin,凭据就带着管理员权限工作。已经是 admin 的账号(存量)不在
// 这里追溯 —— 只挡"加"这个动作。
func (s *Services) GuardAdminGrant(target *model.User, next []int64) error {
	if target == nil || !s.roleIDsIncludeAdmin(next) {
		return nil
	}
	if s.roleIDsIncludeAdmin(s.Repo.EffectiveRoleIDs(target)) {
		return nil // 已经是管理员:不是"新加",存量不追溯
	}
	if target.Kind == model.UserKindService {
		return &AdminGrantRefusal{err: apimsg.Errorf("服务账号 %s 不能带平台管理员角色:它的唯一入口是 API 凭据", target.Name)}
	}
	clients, _ := s.Repo.ListAPIClients()
	var names []string
	for _, c := range clients {
		if c.UserID == target.ID && c.Enabled {
			names = append(names, "「"+c.Name+"」")
		}
	}
	if len(names) > 0 {
		return &AdminGrantRefusal{err: apimsg.Errorf("%s 正被启用中的 API 凭据 %s 绑定,加上平台管理员会让这些凭据带着管理员权限工作。请先在「开放接口」停用或改绑这些凭据",
			target.Name, strings.Join(names, ""))}
	}
	return nil
}
