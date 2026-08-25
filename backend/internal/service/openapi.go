package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/model"
	"velagateway/pkg/crypto"
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
		return nil, "", fmt.Errorf("凭据名称不能为空")
	}
	svcUser, err := s.Repo.GetUserByID(req.UserID)
	if err != nil {
		return nil, "", fmt.Errorf("服务账号不存在")
	}
	if svcUser.Status != "active" {
		return nil, "", fmt.Errorf("服务账号 %s 未启用", svcUser.Name)
	}
	scopes := normalizeScopes(req.Scopes)
	key, secret, err := newAPICredential()
	if err != nil {
		return nil, "", err
	}
	hash, err := crypto.HashPassword(secret)
	if err != nil {
		return nil, "", err
	}
	cl := &model.APIClient{
		Name: clip(name, 60), Key: key, SecretHash: hash,
		UserID: svcUser.ID, UserName: svcUser.Name,
		AllowIPs: strings.TrimSpace(req.AllowIPs), Scopes: strings.Join(scopes, ","),
		Enabled: true, CreatedBy: actor.ID,
	}
	if err := s.Repo.CreateAPIClient(cl); err != nil {
		return nil, "", err
	}
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
func (s *Services) UpdateAPIClient(id int64, req dto.APIClientReq) (*model.APIClient, error) {
	cl, err := s.Repo.GetAPIClient(id)
	if err != nil {
		return nil, ErrNotFound
	}
	fields := map[string]any{"enabled": req.Enabled, "allow_ips": strings.TrimSpace(req.AllowIPs)}
	if n := strings.TrimSpace(req.Name); n != "" {
		fields["name"] = clip(n, 60)
	}
	if len(req.Scopes) > 0 {
		fields["scopes"] = strings.Join(normalizeScopes(req.Scopes), ",")
	}
	if req.UserID > 0 && req.UserID != cl.UserID {
		u, uerr := s.Repo.GetUserByID(req.UserID)
		if uerr != nil {
			return nil, fmt.Errorf("服务账号不存在")
		}
		fields["user_id"], fields["user_name"] = u.ID, u.Name
	}
	if err := s.Repo.UpdateAPIClientFields(id, fields); err != nil {
		return nil, err
	}
	return s.Repo.GetAPIClient(id)
}

func (s *Services) DeleteAPIClient(id int64) error {
	if _, err := s.Repo.GetAPIClient(id); err != nil {
		return ErrNotFound
	}
	return s.Repo.DeleteAPIClient(id)
}

// normalizeScopes keeps only scopes this build knows. An unknown scope string
// would look like a granted permission in the console and match nothing at the
// guard, which is the worst of both readings.
func normalizeScopes(in []string) []string {
	known := map[string]bool{}
	for _, s := range model.AllScopes {
		known[s] = true
	}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if known[s] && !contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return model.AllScopes
	}
	return out
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
	pipelineID, err := s.resolvePipelineID(req.PipelineID, req.Pipeline)
	if err != nil {
		return nil, err
	}

	inner := dto.ReleaseReq{
		Title: strings.TrimSpace(req.Title), PipelineID: pipelineID,
		ConnectionID: conn.ID, Database: strings.TrimSpace(req.Database),
		Reason: strings.TrimSpace(req.Reason), MfaCode: req.MfaCode,
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
			return nil, fmt.Errorf("脚本落盘失败: %w", uerr)
		}
		inner.ScriptUploadID = up.ID
	case strings.TrimSpace(req.SQL) != "":
		inner.SQL = req.SQL
	default:
		return nil, fmt.Errorf("请提供 sql 或 script 内容")
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
					return "", "", fmt.Errorf("scriptBase64 不是合法的 base64")
				}
			}
		}
		return string(raw), filename, nil
	}
	if strings.TrimSpace(req.Script) != "" {
		return req.Script, filename, nil
	}
	return "", filename, nil
}

// resolveConnection accepts an id or a NAME, because an external system knows
// the instance it deploys to by name, not by this gateway's row id. Both the
// bare name ("order-cluster") and the console's display form ("prod-order-cluster")
// resolve; an ambiguous or unknown name is an error rather than a guess.
func (s *Services) resolveConnection(id int64, name string) (*model.Connection, error) {
	if id > 0 {
		conn, err := s.Repo.GetConnection(id)
		if err != nil {
			return nil, fmt.Errorf("实例不存在: %d", id)
		}
		return conn, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("请提供 instance(实例名)或 connectionId")
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
		return nil, fmt.Errorf("实例不存在: %s", name)
	}
	if len(hits) > 1 {
		// Instance names are unique per gateway, but the env-prefixed form could
		// in principle match two rows; refuse rather than pick one, because the
		// wrong pick here is a change applied to the wrong database.
		return nil, fmt.Errorf("实例名 %s 不唯一,请改用 connectionId", name)
	}
	return &hits[0], nil
}

// resolvePipelineID accepts an id or a flow NAME; zero means "the default flow
// for the target's tier", which submitRelease works out.
func (s *Services) resolvePipelineID(id int64, name string) (int64, error) {
	if id > 0 {
		return id, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	ps, err := s.Repo.ListPipelines()
	if err != nil {
		return 0, err
	}
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("发布流程不存在: %s", name)
}

// ---------------------------------------------------------------- 查询

// ReleaseStatusForClient returns one ticket by its number, scoped to the client
// that raised it.
//
// A credential can read the releases its own system created, not everything on
// the gateway: an integration that can see every production change is an
// information leak that no integrator asked for. A client whose service account
// holds an oversight role sees more, through the same rule the console uses.
func (s *Services) ReleaseStatusForClient(u *model.User, cl *model.APIClient, relNo string) (*dto.ReleaseView, error) {
	rel, err := s.Repo.GetReleaseByNo(strings.TrimSpace(relNo))
	if err != nil {
		return nil, ErrNotFound
	}
	own := cl != nil && rel.ClientID == cl.ID
	if !own && rel.CreatorID != u.ID && !s.canSeeAllActivity(u) {
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
	return s.AbortRelease(u, rel.ID)
}

// OpenReleaseResp renders a release in the external contract shape. withLog is
// false on the create response (there is nothing to report yet) and true on a
// status poll, which is where a caller looks to find out WHY a stage failed.
func (s *Services) OpenReleaseResp(rel *model.Release, withLog bool) dto.OpenReleaseResp {
	v := s.releaseView(*rel) // redacts credentials on the way out
	out := dto.OpenReleaseResp{
		RelNo: v.RelNo, Title: v.Title, Status: v.Status, Risk: v.Risk,
		Instance: v.Instance, Database: v.Database, Env: v.Env, Pipeline: v.PipelineName,
		ExternalRef: v.ExternalRef, Error: v.Error,
		CreatedAt: v.CreatedAt.Format(time.RFC3339),
		Stages:    make([]dto.OpenStage, 0, len(v.Stages)),
	}
	if v.FinishedAt != nil {
		out.FinishedAt = v.FinishedAt.Format(time.RFC3339)
	}
	for _, st := range v.Stages {
		one := dto.OpenStage{
			Order: st.StepOrder, Name: st.Name, Type: st.Type,
			Status: st.Status, ApprovalNo: st.ApprovalNo,
		}
		if withLog {
			one.Log = st.Log
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
func (s *Services) CheckSQLForClient(u *model.User, req dto.OpenReviewReq) (*dto.ReviewCheckResp, error) {
	sql := req.SQL
	if body, _, err := scriptFromRequest(dto.OpenReleaseReq{
		Script: req.Script, ScriptBase64: req.ScriptBase64, Filename: req.Filename,
	}); err != nil {
		return nil, err
	} else if body != "" {
		sql = body
	}
	if strings.TrimSpace(sql) == "" {
		return nil, fmt.Errorf("请提供 sql 或 script 内容")
	}
	connID := req.ConnectionID
	if connID == 0 && strings.TrimSpace(req.Instance) != "" {
		conn, err := s.resolveConnection(0, req.Instance)
		if err != nil {
			return nil, err
		}
		connID = conn.ID
	}
	return s.CheckSQL(u, connID, req.Dialect, sql)
}

// APIClientLastUsed is a display helper for the console listing.
func APIClientLastUsed(c model.APIClient) string {
	if c.LastUsedAt == nil {
		return ""
	}
	return c.LastUsedAt.Format(time.RFC3339)
}
