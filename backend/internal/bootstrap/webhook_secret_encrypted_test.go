package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
)

// M4(2026-10 安全审计):Webhook 推送口令明文落库。改为密文入库,读出来(推送时)仍是明文;
// 旧的明文行照常可用(TestWebhook_TestSendCarriesBearerTokenAndPlatformSchema 覆盖推送带口令)。
func TestWebhook_SecretStoredEncrypted(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer stub.Close()
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	const secret = "webhook-plain-secret-m4"
	app.configureWebhook(token, stub.URL, secret, "delete", 1)

	var row model.WebhookConfig
	if err := app.repo.DB().Order("id asc").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Secret == secret {
		t.Fatal("推送口令不应明文落库")
	}
	if plain, err := crypto.DecryptSecret(row.Secret); err != nil || plain != secret {
		t.Fatalf("库里的应是这条口令的密文,解出 %q err=%v", plain, err)
	}
	got, err := app.repo.GetWebhook()
	if err != nil || got.Secret != secret {
		t.Fatalf("读出来应是明文,got %q err=%v", got.Secret, err)
	}

	// 旧的明文行:照常读得出
	if err := app.repo.DB().Model(&model.WebhookConfig{}).Where("id = ?", row.ID).Update("secret", "legacy-plain").Error; err != nil {
		t.Fatal(err)
	}
	if got, _ := app.repo.GetWebhook(); got.Secret != "legacy-plain" {
		t.Errorf("旧明文行应原样读出,got %q", got.Secret)
	}
}
