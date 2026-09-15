package service

import (
	"strconv"
	"strings"
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// 建 n 行真实的审计链,返回它们的 id。
func seedChain(t *testing.T, s *Services, n int) []int64 {
	t.Helper()
	actor := &model.User{ID: 1, Name: "林薇"}
	var ids []int64
	for i := 0; i < n; i++ {
		a := s.appendAudit(actor, nil, "select "+strconv.Itoa(i), "low", "ok", "", "linwei")
		if a == nil || a.ID == 0 {
			t.Fatalf("appendAudit #%d 没有落行", i)
		}
		ids = append(ids, a.ID)
	}
	return ids
}

func auditActorUser(t *testing.T, s *Services) *model.User {
	t.Helper()
	r := &model.Role{Code: model.RoleAdmin, Name: "管理员", Layer: "platform"}
	if err := s.Repo.DB().Create(r).Error; err != nil {
		t.Fatalf("seed role: %v", err)
	}
	u := &model.User{Name: "林薇", Email: "linwei@vela.io", RoleID: r.ID, Status: "active"}
	if err := s.Repo.DB().Create(u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// 不设起点时,行为与从前一模一样:整条链从创世行查到链尾。
func TestVerifyAuditChain_WithoutASegmentStillChecksEverything(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	u := auditActorUser(t, s)
	ids := seedChain(t, s, 5)

	rep, err := s.VerifyAuditChain(u)
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if !rep.OK || rep.Checked != 5 {
		t.Fatalf("整链应当完好且查了 5 行,实际 ok=%v checked=%d reason=%q", rep.OK, rep.Checked, rep.Reason)
	}
	if rep.FirstID != ids[0] {
		t.Errorf("起点 = %d, 应当是创世行 %d", rep.FirstID, ids[0])
	}
}

// 设了起点之后,起点之前那段不再参与 —— 哪怕它被改过。
//
// 这正是这个功能存在的理由:从旧库搬过来的审计行按旧算法签名,任何版本都验不过
// (ADR 0019 §3),而一条永远报红的链等于没有链 —— 它只会训练人忽略那个警报。
func TestVerifyAuditChain_SegmentSkipsTheRowsBeforeIt(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	u := auditActorUser(t, s)
	ids := seedChain(t, s, 5)

	// 把第 2 行的内容改掉 —— 不设起点的话这必然报红。
	if err := db.Model(&model.AuditLog{}).Where("id = ?", ids[1]).
		Update("command", "被人改过的内容").Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if rep, _ := s.VerifyAuditChain(u); rep.OK {
		t.Fatal("前提不成立:改了第 2 行,不设起点时本应报红")
	}

	// 起点设在第 3 行。
	if err := s.Repo.SetSettings(map[string]string{
		auditVerifyFromKey: strconv.FormatInt(ids[2], 10),
	}); err != nil {
		t.Fatalf("set segment: %v", err)
	}

	rep, err := s.VerifyAuditChain(u)
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if !rep.OK {
		t.Fatalf("起点之后那段没被动过,应当完好,实际 brokenId=%d reason=%q", rep.BrokenID, rep.Reason)
	}
	if rep.Checked != 3 {
		t.Errorf("checked = %d, 应当只查起点及之后的 3 行", rep.Checked)
	}
	if rep.FirstID != ids[2] {
		t.Errorf("FirstID = %d, 应当是起点 %d", rep.FirstID, ids[2])
	}
}

// 报告必须说出它没查什么。
//
// 这是整个功能的良心所在:分段之后「链完好」这四个字的含义变小了 —— 它只covers
// 起点之后。报告若不说,读它的人会按原来的含义去理解,而那正是 Note 这个字段
// 当初被加出来要防的事(见 chainNote:一句"链完好"如果让人以为末尾截断也查得出来,
// 那它就成了假的安全感)。
func TestVerifyAuditChain_SegmentIsDisclosedInTheReport(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	u := auditActorUser(t, s)
	ids := seedChain(t, s, 5)

	if err := s.Repo.SetSettings(map[string]string{
		auditVerifyFromKey: strconv.FormatInt(ids[2], 10),
	}); err != nil {
		t.Fatalf("set segment: %v", err)
	}

	rep, err := s.VerifyAuditChain(u)
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if rep.VerifiedFromID != ids[2] {
		t.Errorf("VerifiedFromID = %d, 应当是 %d", rep.VerifiedFromID, ids[2])
	}
	if !strings.Contains(rep.Note, strconv.FormatInt(ids[2], 10)) {
		t.Errorf("Note 没有点明起点行号,读报告的人会把「链完好」当成全量完好:\n  %s", rep.Note)
	}
}
