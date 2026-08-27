package repository_test

// 规则库播种的两条新行为,都会**改已有的行**,所以边界必须钉死:
//
//   - 出处会回填(它是关于规范的事实,不是运营决定)
//   - 已经没有实现的内置规则会被删掉(留着就是一条永远不报的假规则)
//
// 而运维真正拥有的东西 —— 级别、开关、参数、自定义规则 —— 一个字节都不能动。
// 升级悄悄把某条降级过的规则调回 error,会让本来放行的发布单突然全被拦住。

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/review"
)

func seedRepo(t *testing.T) (*repository.Repo, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=busy_timeout(5000)"),
		&gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrator().DropTable(&model.SQLReviewRule{}); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := db.AutoMigrate(&model.SQLReviewRule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repository.New(db), db
}

func ruleByCode(t *testing.T, db *gorm.DB, code string) (model.SQLReviewRule, bool) {
	t.Helper()
	var r model.SQLReviewRule
	if err := db.Where("code = ?", code).First(&r).Error; err != nil {
		return model.SQLReviewRule{}, false
	}
	return r, true
}

// 一条在"出处"这两列存在之前就种下的规则,升级后必须拿到出处 —— 否则整个
// "每条规则都能溯源"的意义就只对全新安装成立。
func TestSeedBackfillsProvenanceOntoRulesThatPredateIt(t *testing.T) {
	repo, db := seedRepo(t)

	// 找一条确实带出处的内置规则,模拟它在旧版本里被种下的样子(出处为空)。
	var withSpec review.Builtin
	for _, b := range review.Builtins {
		if b.Spec != "" && b.SpecRef != "" {
			withSpec = b
			break
		}
	}
	if withSpec.Code == "" {
		t.Fatal("规则库里没有一条带出处的内置规则")
	}
	if err := db.Create(&model.SQLReviewRule{
		Code: withSpec.Code, Name: withSpec.Name, Dialect: withSpec.Dialect,
		Category: withSpec.Category, Level: model.ReviewWarn, // 运维把它降过级
		Kind: model.ReviewKindBuiltin, Enabled: false, // 而且停用了
		Params: `{"max":99}`, // 还改过参数
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("插入旧行: %v", err)
	}
	// enabled 带 default:true,Create 时零值会让位给库默认值 —— 停用要显式写一次。
	if err := db.Model(&model.SQLReviewRule{}).Where("code = ?", withSpec.Code).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("停用: %v", err)
	}

	if _, err := repo.SeedSQLReviewRules(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, ok := ruleByCode(t, db, withSpec.Code)
	if !ok {
		t.Fatal("规则不见了")
	}
	if got.Spec != withSpec.Spec || got.SpecRef != withSpec.SpecRef {
		t.Errorf("出处没回填:spec=%q ref=%q,应为 %q / %q", got.Spec, got.SpecRef, withSpec.Spec, withSpec.SpecRef)
	}
	// 运维的三项决定必须原样保留。
	if got.Level != model.ReviewWarn {
		t.Errorf("升级把级别改回了 %q —— 本来放行的发布单会突然被拦住", got.Level)
	}
	if got.Enabled {
		t.Error("升级把停用的规则重新启用了")
	}
	if got.Params != `{"max":99}` {
		t.Errorf("升级覆盖了运维改过的参数: %q", got.Params)
	}
}

// 内置规则的适用范围随版本走,不是运维的东西(SaveReviewRule 本来就不让改)。
// 不同步的后果很具体:某条规则在新版里收窄了方言,而库里的老行还是 all,
// 同一个字段就会被两条规则各报一次。
func TestSeedNarrowsDialectScopeOnExistingBuiltins(t *testing.T) {
	repo, db := seedRepo(t)

	var scoped review.Builtin
	for _, b := range review.Builtins {
		if b.Dialect != review.DialectAll {
			scoped = b
			break
		}
	}
	if scoped.Code == "" {
		t.Fatal("规则库里没有一条限定方言的内置规则")
	}
	// 模拟上一版:同一个码,但方言还是 all。
	if err := db.Create(&model.SQLReviewRule{
		Code: scoped.Code, Name: scoped.Name, Dialect: review.DialectAll,
		Category: scoped.Category, Level: scoped.Level, Kind: model.ReviewKindBuiltin,
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("插入旧行: %v", err)
	}

	if _, err := repo.SeedSQLReviewRules(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, _ := ruleByCode(t, db, scoped.Code)
	if got.Dialect != scoped.Dialect {
		t.Errorf("方言没跟着收窄:%q,应为 %q —— 老行会在不该跑的方言上重复报告",
			got.Dialect, scoped.Dialect)
	}
}

// 规范改版后下线的内置规则码要清掉。留着它,库里就有一条列得出来、看着启用、
// 实际什么都不查的规则 —— 从外面看,它和"永远通过"没有区别。
func TestSeedDropsBuiltinRulesThatNoLongerShip(t *testing.T) {
	repo, db := seedRepo(t)

	if err := db.Create(&model.SQLReviewRule{
		Code: "dws.count.one", Name: "上一版规范里的规则", Dialect: "dws",
		Category: "dml", Level: model.ReviewError, Kind: model.ReviewKindBuiltin,
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("插入过期规则: %v", err)
	}
	// 运维自己写的规则,不管它的编码长什么样,都不能被当成"下线的内置规则"删掉。
	if err := db.Create(&model.SQLReviewRule{
		Code: "custom.no.select.into", Name: "运维自己的规则", Dialect: "all",
		Category: "dml", Level: model.ReviewWarn, Kind: model.ReviewKindRegex,
		Enabled: true, Params: `{"pattern":"SELECT\\s+INTO"}`,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("插入自定义规则: %v", err)
	}

	if _, err := repo.SeedSQLReviewRules(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, ok := ruleByCode(t, db, "dws.count.one"); ok {
		t.Error("已下线的内置规则仍在库里 —— 它会列出来却什么都不查")
	}
	if _, ok := ruleByCode(t, db, "custom.no.select.into"); !ok {
		t.Error("运维自己的规则被删了")
	}
}

// 播种后,库里的每一条内置规则都必须有对应的实现。这条是前面两条的总闸:
// 无论怎么升级、怎么改版,"列在库里"和"真的会跑"不能脱节。
func TestEveryBuiltinRuleInTheLibraryHasAnImplementation(t *testing.T) {
	repo, db := seedRepo(t)
	if _, err := repo.SeedSQLReviewRules(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var rows []model.SQLReviewRule
	if err := db.Where("kind = ?", model.ReviewKindBuiltin).Find(&rows).Error; err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("播种后库里没有内置规则")
	}
	shipped := map[string]bool{}
	for _, b := range review.Builtins {
		shipped[b.Code] = true
	}
	for _, row := range rows {
		if !shipped[row.Code] {
			t.Errorf("库里的 %s 已不在规则库代码中", row.Code)
		}
	}
	if len(rows) != len(review.Builtins) {
		t.Errorf("库里 %d 条,代码里 %d 条", len(rows), len(review.Builtins))
	}
}
