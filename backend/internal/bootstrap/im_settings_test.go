package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestIMSettings_StatusListsChannels(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.runningFake(admin)

	r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil)
	eq(t, r.Code, 0, "status")
	var rows []struct {
		Kind       string `json:"kind"`
		CanApprove bool   `json:"canApprove"`
		Approval   bool   `json:"approval"`
		Running    bool   `json:"running"`
	}
	_ = json.Unmarshal(r.Data, &rows)
	var sawLark, sawDingTalk, sawSlack, sawFake bool
	for _, row := range rows {
		switch row.Kind {
		case "lark":
			sawLark = true
			if !row.CanApprove {
				t.Error("lark does interactive approval since phase 3")
			}
		case "dingtalk":
			sawDingTalk = true
			if !row.CanApprove {
				t.Error("dingtalk does interactive approval since phase 4")
			}
		case "slack":
			sawSlack = true
			if !row.CanApprove {
				t.Error("slack does interactive approval since phase 5")
			}
		case "fake":
			sawFake = row.Approval && row.Running
		}
	}
	if !sawLark || !sawDingTalk || !sawSlack || !sawFake {
		t.Fatalf("status = %s", r.Data)
	}
}

func TestIMSettings_TestCardGoesToTheAdminsOwnAccountAndTouchesNoTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.runningFake(admin)

	var out struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	r := app.do(http.MethodPost, "/api/v1/settings/im/fake/test-card", admin, nil)
	_ = json.Unmarshal(r.Data, &out)
	if out.Ok || !strings.Contains(out.Message, "绑定") {
		t.Fatalf("unbound admin: %+v — should be told to bind first", out)
	}

	app.bindIM("linwei@vela.io", "fake", "p-lw")
	r = app.do(http.MethodPost, "/api/v1/settings/im/fake/test-card", admin, nil)
	_ = json.Unmarshal(r.Data, &out)
	if !out.Ok {
		t.Fatalf("test card: %+v", out)
	}
	app.flushIM()
	sent := f.Sent()
	if len(sent) != 1 || sent[0].PlatformUserID != "p-lw" || sent[0].Card.ApNo != "AP-TEST" {
		t.Fatalf("sent = %+v", sent)
	}
	var cards int64
	app.repo.DB().Table("tbl_im_card").Count(&cards)
	eq(t, cards, int64(0), "a test card is not recorded — there is nothing to close")

	if res := f.Click("p-lw", "AP-TEST", true); !strings.Contains(res.Toast, "测试卡片") {
		t.Fatalf("toast = %q", res.Toast)
	}
}
