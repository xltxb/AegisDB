package bootstrap

import (
	"io/fs"
	"testing"

	"gorm.io/gorm"

	"velagateway/internal/testsupport"
	"velagateway/migrations"
)

// rerunMigration 在一个已迁移到最新的库上把某个迁移文件再执行一遍。
//
// testsupport.NewDB 已经跑过全部迁移,此刻库里没有任何旧数据可搬。测试的做法是:先按
// 旧版本的样子塞进旧数据,再单独重放这个文件 —— 这要求迁移本身可重入(本期三个迁移都是)。
func rerunMigration(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	raw, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	for _, stmt := range splitSQLStatements(string(raw)) {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v\n%s", name, err, stmt)
		}
	}
}

func settingRows(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var rows []struct{ K, V string }
	if err := db.Raw(`SELECT k, v FROM tbl_setting`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.K] = r.V
	}
	return out
}

func TestMigration0006_MovesLarkNotifyKeys(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Exec(`INSERT INTO tbl_setting (k, v) VALUES
		('notify.lark', 'false'), ('notify.larkWebhook', '"https://open.feishu.cn/hook/x"'),
		('notify.larkSecret', '"plain-secret"'), ('notify.larkChannel', '""')`)
	rerunMigration(t, db, "0006_im_lark_notify_keys.sql")

	got := settingRows(t, db)
	eq(t, got["im.lark.notify"], "false", "the old switch value is kept")
	eq(t, got["im.lark.notifyWebhook"], `"https://open.feishu.cn/hook/x"`, "webhook moved")
	eq(t, got["im.lark.notifySecret"], `"plain-secret"`, "secret moved as-is")
	for _, k := range []string{"notify.lark", "notify.larkWebhook", "notify.larkSecret", "notify.larkChannel"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be gone", k)
		}
	}
}

func TestMigration0006_MissingSwitchWithWebhookMeansOn(t *testing.T) {
	db := testsupport.NewDB(t)
	db.Exec(`INSERT INTO tbl_setting (k, v) VALUES ('notify.larkWebhook', '"https://open.feishu.cn/hook/x"')`)
	rerunMigration(t, db, "0006_im_lark_notify_keys.sql")
	eq(t, settingRows(t, db)["im.lark.notify"], "true",
		"old default was on: a deployment with only a webhook must keep notifying")
}

func TestMigration0006_NoWebhookWritesNoSwitch(t *testing.T) {
	db := testsupport.NewDB(t)
	rerunMigration(t, db, "0006_im_lark_notify_keys.sql")
	if _, ok := settingRows(t, db)["im.lark.notify"]; ok {
		t.Fatal("nothing configured → nothing switched on")
	}
}
