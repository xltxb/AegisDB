package service

import (
	"encoding/json"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// 群卡片是命令离开网关的出口之一:口令字面量不能原样发到飞书群里,
// 但评审的人仍得看得出是哪条语句。
func TestLarkApprovalCardRedactsPassword(t *testing.T) {
	ap := &model.Approval{ApNo: "AP-1", Command: "CREATE USER a IDENTIFIED BY 'p@ss-In-Card'", RiskLevel: "high"}
	raw, err := json.Marshal(larkApprovalCard(ap, "", LoadBrand(nil).Text(BrandDefaultLegacy, false)))
	if err != nil {
		t.Fatal(err)
	}
	card := string(raw)
	if strings.Contains(card, "p@ss-In-Card") {
		t.Errorf("卡片带出了口令明文:%s", card)
	}
	if !strings.Contains(card, "CREATE USER a IDENTIFIED BY") {
		t.Errorf("卡片应仍写明语句本身:%s", card)
	}
}
