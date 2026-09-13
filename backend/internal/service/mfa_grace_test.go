package service

import (
	"testing"
	"time"

	"velagateway/internal/model"
)

// 角色变了,之前那次 MFA 验证就不再算数。
//
// 宽限的意思是「这个会话刚刚证明过自己」,而它证明的是**那时那个角色**:一个刚被提到
// 能写生产库的人,不该靠十分钟前为只读操作做的那次验证就直接下发。
//
// mfaGraceKey 的注释一直声称角色变更会作废它(靠 token 代次),而多角色那一支从不 bump
// —— 那句话一直是空的。现在由 voidMFAGraceFor 精确做掉这件事,而不是把人踢下线。
func TestVoidMFAGraceFor_ClearsEveryInstanceOfThatUser(t *testing.T) {
	s := &Services{mfaGrace: map[string]time.Time{}}
	u := &model.User{ID: 7, TokenVersion: 3}
	other := &model.User{ID: 8, TokenVersion: 1}
	connA := &model.Connection{ID: 11}
	connB := &model.Connection{ID: 22}

	s.noteMFAVerified(u, connA)
	s.noteMFAVerified(u, connB)
	s.noteMFAVerified(other, connA)

	s.voidMFAGraceFor(u.ID)

	if _, ok := s.mfaGrace[mfaGraceKey(u, connA)]; ok {
		t.Error("换角色之后,这台实例上的宽限还在")
	}
	if _, ok := s.mfaGrace[mfaGraceKey(u, connB)]; ok {
		t.Error("宽限是按实例记的,作废要作废他**所有**实例上的 —— 漏一台就是留一个后门")
	}
	if _, ok := s.mfaGrace[mfaGraceKey(other, connA)]; !ok {
		t.Error("别人的宽限被顺手清掉了 —— 那会让不相干的人平白多验证一次")
	}
}
