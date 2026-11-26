package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"gorm.io/gorm"

	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/pkg/crypto"
)

// Seed inserts demo data mirroring the prototype. The full dataset is written
// only on an empty DB, but additive blocks (e.g. the schema tree) are backfilled
// idempotently on every boot so DBs created before a seed addition self-heal.
// Default login: linwei@aegisdb.io / aegisdb123 (platform admin).
func Seed(repo *repository.Repo, cfg *Config) error {
	if repo.Count(&model.Role{}) == 0 {
		// The demo seed creates a platform administrator whose password is
		// published in the README. config.yaml ships with seed: true and its
		// header suggests running `APP_ENV=prod ./server` with it, so without this
		// guard an empty production database gets a publicly-known super user
		// (ED2). Production is initialised by `server init`, which demands a
		// strong password. Refuse loudly rather than seed a weaker path.
		//
		// 只拦 prod 不够:APP_ENV=staging / uat 归一后 Env 是 dev,照样会种(2026-10 安全审计 M6)。
		// 改为只有本地开发(IsLocalDev:原始环境名为空 / dev / local)才种。
		if !cfg.IsLocalDev() {
			return fmt.Errorf("refusing to seed demo data outside local development (env %q): set database.seed=false and run `server init` to create the first administrator", cfg.EnvRaw)
		}
		if err := seedFreshData(repo, cfg); err != nil {
			return err
		}
	}
	if err := seedGliEnv(repo); err != nil {
		return err
	}
	if err := backfillEnvTiers(repo.DB()); err != nil {
		return err
	}
	return seedSchema(repo)
}

// builtinTiers are the five control tiers this product ships with. The tier —
// not the environment's name — is what says which KIND of environment an
// instance sits in, and it is what every rule row is keyed by.
//
// The flags reproduce the behaviour that used to be `== model.EnvProd` tests
// scattered through the gateway. Only prod carries them: gli holds real legal
// data but is deliberately left on the same (loose) control profile it has
// always had, so this correction changes no verdict on any existing instance.
//
// The display names were WRONG until 2026-08-06 — gli was seeded as 灰度 and
// staging as 演练UAT — and the wrong names had spread into comments, tests and
// the migration. The names here are the authority; see 0014 for the upgrade.
//
// ConnLayer/DefaultRole are display defaults only (nothing about access control
// reads them) and are editable per tier in the console, so the layer strings
// below are a starting point rather than a claim about anyone's L-numbering.
// StrictNoWhere is spelled out on every row, including the ones that set it
// false. It must never be left to the zero value: a tier created with the flag
// unset would come up with the no-WHERE gate OFF, and a gate that is missing is
// the one failure this file cannot afford (ED1).
//
// Every tier, DEV included, starts with the gate ON (2026-11-16 用户定:以 MySQL 为准)。
// 从前 DEV 在这里是刻意的 false,而生产走迁移 0030 新装 / 升级出来的 DEV 是 true ——
// 同一个产品两种初始状态,本地环境测出的行为在生产上对不上。现在两条路一致:
// 新装时所有分层都拦无 WHERE 的 DELETE / UPDATE;要放行 DEV,在【环境分层】里取消勾选,
// 那是运维的一次显式操作,不是安装方式带来的差别。
var builtinTiers = []model.EnvTier{
	{Code: model.EnvProd, DisplayName: "生产环境 · PROD", SortOrder: 0,
		RequireMFA: true, DangerBanner: true, CountsInPending: true, ScanBaseline: true,
		StrictNoWhere: true, MaskSensitive: true, BizExportRestrict: true, ConnLayer: "L1 核心 · 写", DefaultRole: "dba_l2"},
	{Code: model.EnvGli, DisplayName: "法务环境 · GLI", SortOrder: 1,
		StrictNoWhere: true, MaskSensitive: true, BizExportRestrict: true, ConnLayer: "L2 法务", DefaultRole: "dba_l2"},
	{Code: model.EnvStaging, DisplayName: "预发布环境 · STAGING", SortOrder: 2,
		StrictNoWhere: true, MaskSensitive: true, BizExportRestrict: true, ConnLayer: "L3 预发布", DefaultRole: "dba_l2"},
	{Code: model.EnvUat, DisplayName: "演练环境 · UAT", SortOrder: 3,
		StrictNoWhere: true, MaskSensitive: false, BizExportRestrict: false, AutoExecute: true, ConnLayer: "L3 演练", DefaultRole: "dba_l2"},
	{Code: model.EnvDev, DisplayName: "开发环境 · DEV", SortOrder: 4,
		StrictNoWhere: true, MaskSensitive: false, BizExportRestrict: false, AutoExecute: true, ConnLayer: "L4 沙盒", DefaultRole: "developer"},
}

// backfillEnvTiers initialises the tier/environment split on a database that
// predates it. Like backfillGliEnv it also runs from the `migrate` path, because
// production upgrades run `migrate` while `seed` is first-install only.
//
// One environment is created per tier, NAMED AFTER IT. That is the whole reason
// this migration moves no data: every existing tbl_connection.env already holds
// one of these four strings, so those rows are already valid environment codes,
// and tbl_role_capability.env / tbl_risk_command.env already hold tier codes.
//
// Seeded only when the table is EMPTY, not row-by-row. A per-row backfill would
// resurrect a tier the operator deliberately deleted on the next restart.
func backfillEnvTiers(db *gorm.DB) error {
	var tiers int64
	if err := db.Model(&model.EnvTier{}).Count(&tiers).Error; err != nil {
		return err
	}
	if tiers == 0 {
		for _, t := range builtinTiers {
			// Read the intended value BEFORE the insert: Create both omits a
			// zero-valued `default`-tagged field AND writes the applied default back
			// into the struct, so after it runs t.StrictNoWhere reports true even for
			// DEV, which seeds the no-WHERE gate OFF. Same reason as
			// Repo.CreateEnvTierFrom.
			// MaskSensitive / BizExportRestrict 同理(dev、uat 种成关)。
			want := map[string]any{"strict_nowhere": t.StrictNoWhere, "mask_sensitive": t.MaskSensitive,
				"biz_export_restrict": t.BizExportRestrict}
			if err := db.Create(&t).Error; err != nil {
				return err
			}
			if err := db.Model(&model.EnvTier{}).Where("code = ?", t.Code).
				Updates(want).Error; err != nil {
				return err
			}
		}
	}

	var envs int64
	if err := db.Model(&model.Environment{}).Count(&envs).Error; err != nil {
		return err
	}
	if envs == 0 {
		for _, t := range builtinTiers {
			if err := db.Create(&model.Environment{
				Code: t.Code, DisplayName: t.DisplayName, TierCode: t.Code, SortOrder: t.SortOrder,
			}).Error; err != nil {
				return err
			}
		}
	}
	if err := correctBuiltinTiers(db); err != nil {
		return err
	}
	if err := backfillExplainCapability(db); err != nil {
		return err
	}
	if err := backfillReleaseCapability(db); err != nil {
		return err
	}
	if err := backfillEnvTierMenu(db); err != nil {
		return err
	}
	// The release menu key follows the same rule as the tiers menu did: a key with
	// no RoleMenu row reads as denied, so an upgraded database would show the
	// permissions page a menu nobody — not even an administrator — could grant.
	if err := backfillPipelineMenu(db); err != nil {
		return err
	}
	// 同上:execwindow 是这一版新拆出来的键。
	if err := backfillExecWindowMenu(db); err != nil {
		return err
	}
	// 项目菜单同理:没有 RoleMenu 行的键读作对所有人拒绝,管理员也不例外。
	if err := backfillProjectMenu(db); err != nil {
		return err
	}
	// 业务查询角色要在分层与菜单都建好之后再补一次:全新安装时 AutoMigrate 那一次
	// 调用发生在它们之前,那时补不上任何 deny 行(见 ensureBizQueryRole)。
	if err := backfillQueryMenu(db); err != nil {
		return err
	}
	if err := backfillSchemaDiffMenu(db); err != nil {
		return err
	}
	if err := backfillInspectionMenu(db); err != nil {
		return err
	}
	return ensureBizQueryRole(db)
}

// backfillExplainCapability writes the `explain` row for every role × tier that
// has none.
//
// A missing capability row reads as `allow`, so behaviour is identical either
// way — this is about what the permissions page can show. Without the rows it
// renders the default rather than a stored value, so an administrator looking at
// the matrix cannot tell "nobody has decided this" from "somebody chose allow",
// and the first save would be writing values they never actually reviewed.
//
// Per cell, and only where absent: a level an operator has set is never touched.
// Deleting a row means "fall back to the default", and the default is what gets
// written back — same meaning, now visible. Tiers created later get their rows
// from the template clone (Repo.CreateEnvTierFrom), not from here.
func backfillExplainCapability(db *gorm.DB) error {
	return backfillCapability(db, "explain", func(*gorm.DB, model.Role, model.EnvTier) (string, error) {
		return model.LevelAllow, nil
	})
}

// backfillReleaseCapability writes the `release` row (发起发布单) for every role ×
// tier that has none, MIRRORING that role's `write` level on the same tier.
//
// Not defaulted to allow, and not hand-written per role: derived. "May this role
// start a release against this tier" and "may this role change data on it" are
// different questions (see model.CapRelease), but a role that cannot write on a
// tier certainly should not be able to raise a release there, and one that needs
// approval to write should need it to release. Deriving reproduces the intent the
// operator already expressed on that tier instead of inventing a second policy
// they never reviewed — and it is the same table the seeded matrix ships, so the
// two cannot drift (TestReleaseCapabilityMirrorsWrite).
//
// Per cell and only where absent, like every other capability backfill: a level
// somebody set is never touched.
func backfillReleaseCapability(db *gorm.DB) error {
	return backfillCapability(db, model.CapRelease, func(tx *gorm.DB, r model.Role, t model.EnvTier) (string, error) {
		var write model.RoleCapability
		err := tx.Where("role_id = ? AND capability = ? AND tier_code = ?", r.ID, "write", t.Code).
			First(&write).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// No write row means write reads as allow (Repo.CapabilityLevel), so
			// release inherits the same answer.
			return model.LevelAllow, nil
		}
		if err != nil {
			return "", err
		}
		return write.Level, nil
	})
}

// backfillCapability is the shared shape of a capability backfill: for every
// role × tier with no row for `cap`, write one at the level `level` computes.
//
// A missing capability row reads as `allow`, so behaviour is identical either
// way — this is about what the permissions page can SHOW. Without the rows it
// renders the default rather than a stored value, so an administrator looking at
// the matrix cannot tell "nobody has decided this" from "somebody chose allow",
// and the first save would be writing values they never actually reviewed.
//
// Tiers created later get their rows from the template clone
// (Repo.CreateEnvTierFrom), not from here.
func backfillCapability(db *gorm.DB, cap string, level func(*gorm.DB, model.Role, model.EnvTier) (string, error)) error {
	var roles []model.Role
	if err := db.Find(&roles).Error; err != nil {
		return err
	}
	var tiers []model.EnvTier
	if err := db.Find(&tiers).Error; err != nil {
		return err
	}
	for _, r := range roles {
		for _, t := range tiers {
			var n int64
			if err := db.Model(&model.RoleCapability{}).
				Where("role_id = ? AND capability = ? AND tier_code = ?", r.ID, cap, t.Code).
				Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			lvl, err := level(db, r, t)
			if err != nil {
				return err
			}
			if err := db.Create(&model.RoleCapability{
				RoleID: r.ID, Capability: cap, TierCode: t.Code, Level: lvl,
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// wrongBuiltinNames are the display names this product shipped with before the
// five-tier correction. They are kept ONLY so an upgrade can recognise a label it
// wrote itself and replace it — see correctBuiltinTiers.
var wrongBuiltinNames = map[string][]string{
	model.EnvGli:     {"灰度 · GLI"},
	model.EnvStaging: {"演练UAT · STAGING"},
	model.EnvDev:     {"测试 · DEV"},
}

// wrongBuiltinLayers are the matching ConnLayer strings, for the same purpose.
var wrongBuiltinLayers = map[string][]string{
	model.EnvGli:     {"L2 灰度"},
	model.EnvStaging: {"L3 演练UAT"},
}

// correctBuiltinTiers repairs a database seeded before the five-tier correction:
// it adds the missing uat tier and replaces the two mislabelled names.
//
// The labels were not cosmetic errors. "gli" was seeded, documented and tested as
// 灰度 (grey release) when it is the 法务 (legal) environment, and "staging" as
// 演练UAT when it is 预发布 — so anyone reading the console was told the wrong
// thing about which instances they were about to touch. The CODES were right all
// along, which is why no rule row or connection needs to move: an environment's
// name never decided anything, the tier did.
//
// Only a label this code wrote itself is replaced (see wrongBuiltinNames). An
// operator who has renamed a tier keeps their name — overwriting a deliberate
// edit to fix our own mistake would be its own kind of wrong.
func correctBuiltinTiers(db *gorm.DB) error {
	for _, want := range builtinTiers {
		var cur model.EnvTier
		err := db.First(&cur, "code = ?", want.Code).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A built-in that does not exist yet — uat on any pre-correction
			// database. Create it, give it an environment, and clone its rules;
			// a tier without rule rows is an environment nothing governs.
			if err := db.Create(&want).Error; err != nil {
				return err
			}
			if err := db.Create(&model.Environment{
				Code: want.Code, DisplayName: want.DisplayName, TierCode: want.Code, SortOrder: want.SortOrder,
			}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		updates := map[string]any{}
		if slices.Contains(wrongBuiltinNames[want.Code], cur.DisplayName) {
			updates["display_name"] = want.DisplayName
		}
		if slices.Contains(wrongBuiltinLayers[want.Code], cur.ConnLayer) {
			updates["conn_layer"] = want.ConnLayer
		}
		if len(updates) > 0 {
			if err := db.Model(&model.EnvTier{}).Where("code = ?", want.Code).Updates(updates).Error; err != nil {
				return err
			}
		}
		// The identically-named environment carries the same label, and was
		// created from it — correct it on the same terms.
		var env model.Environment
		if err := db.First(&env, "code = ?", want.Code).Error; err == nil {
			if slices.Contains(wrongBuiltinNames[want.Code], env.DisplayName) {
				if err := db.Model(&model.Environment{}).Where("code = ?", want.Code).
					Update("display_name", want.DisplayName).Error; err != nil {
					return err
				}
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	// uat's rule rows, whether it was just created here or on a fresh seed.
	return mirrorTierRules(db, model.EnvStaging, model.EnvUat)
}

// backfillEnvTierMenu grants the new "envtier" menu to whoever already holds
// "rules".
//
// A menu key with no RoleMenu row reads as denied (MenuGuard), so on an upgraded
// database nobody — not even an administrator — could open the tiers page or
// call its endpoints, and the only way to grant it would be a page they cannot
// reach. Tiers and environments were managed from the rules menu before they had
// a page of their own, so inheriting that grant keeps the same people in charge
// and gives nobody a permission they did not have.
//
// Written only when the key is entirely absent, so a deliberate revocation is
// not undone on the next restart.
func backfillEnvTierMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", "envtier").Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var rules []model.RoleMenu
	if err := db.Where("menu_key = ?", "rules").Find(&rules).Error; err != nil {
		return err
	}
	for _, r := range rules {
		if err := db.Create(&model.RoleMenu{
			RoleID: r.RoleID, MenuKey: "envtier", Enabled: r.Enabled,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// backfillExecWindowMenu 把新的 execwindow 菜单发给已经持有 terminal 的角色。
//
// 没有 RoleMenu 行的菜单键在 MenuGuard 里读作**拒绝**,所以升级过的库里没有人 ——
// 管理员也不行 —— 能打开这一页,而唯一能授权的地方正是一个他打不开的页面。
// 窗口过去挂在 envtier 菜单下、且只有管理员能建;现在它是"申请"权,跟着 terminal
// 走(见上面 menuMatrix 里的理由)。
//
// 只在这个键**完全不存在**时写,所以一次刻意的收回不会在下次重启时被撤销。
func backfillExecWindowMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", "execwindow").Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var base []model.RoleMenu
	if err := db.Where("menu_key = ?", "terminal").Find(&base).Error; err != nil {
		return err
	}
	for _, r := range base {
		if err := db.Create(&model.RoleMenu{
			RoleID: r.RoleID, MenuKey: "execwindow", Enabled: r.Enabled,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedGliEnv backfills the tiers whose rules are cloned from staging: GLI (法务)
// and, since the five-tier correction, UAT (演练). Both are later additions whose
// capability-matrix and risk-dictionary rows mirror staging's, so their instances
// are gated coherently from day one. Idempotent: clones a staging row only when
// the counterpart is absent, so DBs seeded before either existed self-heal on
// boot (like seedSchema).
//
// GLI was documented here as 灰度 (grey release) until 2026-08-06. It is the 法务
// (legal) environment; only the label was wrong, never the code.
//
// Superseded by the tier model: a new tier now clones its rules through
// Repo.CreateEnvTierFrom, which is this function generalised. Kept on the boot
// path because databases older than GLI still need it — it is a no-op once those
// rows exist, and dropping it would leave them ungoverned (ED1).
func seedGliEnv(repo *repository.Repo) error { return backfillGliEnv(repo.DB()) }

// backfillGliEnv is seedGliEnv against a raw handle, so the migration path can
// run it too (see Migrate).
func backfillGliEnv(db *gorm.DB) error {
	if err := mirrorTierRules(db, model.EnvStaging, model.EnvGli); err != nil {
		return err
	}
	// UAT (演练) arrived with the five-tier correction and is seeded the same way,
	// from the same source: staging is where the rehearsal rules already lived
	// (it used to be LABELLED 演练UAT), so cloning it hands those rules to the tier
	// that now carries that meaning.
	return mirrorTierRules(db, model.EnvStaging, model.EnvUat)
}

// mirrorTierRules copies every capability-matrix and risk-dictionary row from one
// tier to another, writing only the rows the destination is missing.
//
// Row-by-row rather than a bulk insert because it must be safe to re-run: a rule
// an operator has since edited on the destination stays edited. Only ABSENT rows
// are created, and an absent row is the thing that matters — both lookups read
// one as permission granted, so a half-populated tier is an unregulated one.
func mirrorTierRules(db *gorm.DB, src, dst string) error {
	var caps []model.RoleCapability
	if err := db.Where("tier_code = ?", src).Find(&caps).Error; err != nil {
		return err
	}
	for _, row := range caps {
		var n int64
		db.Model(&model.RoleCapability{}).
			Where("role_id = ? AND capability = ? AND tier_code = ?", row.RoleID, row.Capability, dst).
			Count(&n)
		if n == 0 {
			if err := db.Create(&model.RoleCapability{
				RoleID: row.RoleID, Capability: row.Capability, TierCode: dst, Level: row.Level,
			}).Error; err != nil {
				return err
			}
		}
	}

	var cmds []model.RiskCommand
	if err := db.Where("tier_code = ?", src).Find(&cmds).Error; err != nil {
		return err
	}
	for _, r := range cmds {
		var n int64
		db.Model(&model.RiskCommand{}).Where("command = ? AND tier_code = ?", r.Command, dst).Count(&n)
		if n == 0 {
			if err := db.Create(&model.RiskCommand{Command: r.Command, TierCode: dst, Level: r.Level}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// seedReference inserts the reference data every deployment needs: roles, menus,
// the capability matrix, default role tags, the risk dictionary, webhook and
// gateway settings. Shared by the dev demo seed and the production `init`.
func seedReference(repo *repository.Repo, cfg *Config) (map[string]int64, error) {
	db := repo.DB()

	// ---- Roles ----
	roles := []model.Role{
		{Code: "admin", Name: "平台管理员", Layer: "L0 · 全局", Icon: "crown", CanApprove: true, DefaultConnRole: "dba_l2", Description: "管理连接配置、规则、用户与系统设置;可审批高危命令。"},
		{Code: "owner", Name: "DBA 负责人", Layer: "L1 · 终审", Icon: "shield", CanApprove: true, DefaultConnRole: "dba_l2", Description: "高危命令审批链终审节点。"},
		{Code: "l2", Name: "DBA L2", Layer: "L2 · 执行(需审批)", Icon: "user-cog", CanApprove: false, DefaultConnRole: "dba_l2", Description: "可在 PROD 执行常规操作,高危命令需走审批。"},
		{Code: "ro", Name: "研发只读", Layer: "L3 · 只读", Icon: "code", CanApprove: false, DefaultConnRole: "readonly", Description: "仅 Web 命令行查询与审计日志。"},
		{Code: "audit", Name: "审计员", Layer: "L3 · 只看日志", Icon: "eye", CanApprove: false, DefaultConnRole: "readonly", Description: "仅审计日志查看与导出。"},
	}
	if err := db.Create(&roles).Error; err != nil {
		return nil, err
	}
	roleID := map[string]int64{}
	for _, r := range roles {
		roleID[r.Code] = r.ID
	}

	// create routes every reference-row insert through a sticky error so a failed
	// write is surfaced (not silently dropped) and aborts the seed (M13).
	var seedErr error
	create := func(v any) {
		if seedErr == nil {
			seedErr = db.Create(v).Error
		}
	}

	// ---- Menus ----
	menuKeys := []string{"terminal", "approve", "db", "rules", "envtier", "perms", "audit", "settings", "pipeline", "execwindow"}
	// Instance config (db), rules, tiers/environments (envtier), permissions
	// (perms) and settings are all platform-admin only — non-admins don't even
	// see these pages.
	//
	// pipeline (发布流程/CI-CD) follows terminal: raising a release ends in an
	// execution against an instance, so the people who may execute there are the
	// ones who may release. Read-only and audit roles are deliberately left out —
	// they cannot execute, and a release they could raise would only ever be
	// refused at the execute stage.
	// execwindow(执行窗口/班车)跟 terminal 走:申请一个窗口是为了让**自己**接下来
	// 的变更不必逐条等人批,所以能申请的是那些本来就要在这台实例上执行的人。它只是
	// 申请权 —— 批不批由审批链决定,而只读与审计角色跟着 terminal 一起排除在外。
	menuMatrix := map[string][]bool{
		//        terminal approve  db    rules envtier perms audit settings pipeline execwindow
		"admin": {true, true, true, true, true, true, true, true, true, true},
		"owner": {true, true, false, false, false, false, true, false, true, true},
		"l2":    {true, true, false, false, false, false, true, false, true, true},
		"ro":    {true, false, false, false, false, false, true, false, false, false},
		"audit": {false, false, false, false, false, false, true, false, false, false},
	}
	for code, vals := range menuMatrix {
		for i, k := range menuKeys {
			create(&model.RoleMenu{RoleID: roleID[code], MenuKey: k, Enabled: vals[i]})
		}
	}

	// ---- Capability matrix (caps × [prod,staging,dev]) ----
	//
	// "explain" is a dimension of its own rather than part of "select". A
	// plan-only EXPLAIN executes nothing, but it still exposes a statement's
	// schema and row-count statistics — including for statements the role is
	// forbidden to run — so whether a role may read a plan is a separate question
	// from whether it may read data. It is listed last so the rows above keep
	// their positions in the matrices below.
	//
	// Seeded "allow" everywhere, which is exactly the behaviour before it existed.
	// The dimension is here so an operator CAN restrict it, not so the product
	// decides for them.
	// "release" (发起发布单) is the newest dimension and is seeded to mirror who
	// can already change what: the roles that may run a change here may also
	// release one, production releases go through an approval-backed flow, and the
	// read-only roles cannot raise one at all. See model.CapRelease for why it is
	// a dimension of its own rather than an alias of write/ddl.
	caps := model.Capabilities
	envs := []string{"prod", "staging", "dev"}
	matrices := map[string][][]string{
		"admin": {{"allow", "allow", "allow"}, {"approve", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "approve"}, {"allow", "allow", "allow"}, {"allow", "allow", "deny"}, {"allow", "allow", "allow"}, {"approve", "allow", "allow"}},
		"owner": {{"allow", "allow", "allow"}, {"approve", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "approve"}, {"allow", "allow", "allow"}, {"allow", "allow", "allow"}, {"allow", "allow", "allow"}, {"approve", "allow", "allow"}},
		"l2":    {{"allow", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"allow", "allow", "allow"}, {"approve", "approve", "allow"}},
		"ro":    {{"allow", "allow", "allow"}, {"deny", "deny", "allow"}, {"deny", "deny", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"allow", "allow", "allow"}, {"deny", "deny", "allow"}},
		"audit": {{"allow", "allow", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"allow", "allow", "allow"}, {"deny", "deny", "deny"}},
	}
	for code, rows := range matrices {
		for ci, capName := range caps {
			for ei, env := range envs {
				create(&model.RoleCapability{RoleID: roleID[code], Capability: capName, TierCode: env, Level: rows[ci][ei]})
			}
		}
	}

	// ---- Role → tag grants (admin/owner unrestricted = no tags) ----
	roleTags := map[string][]string{
		"l2":    {"orders", "users"},
		"ro":    {"analytics", "readonly"},
		"audit": {"analytics"},
	}
	for code, tags := range roleTags {
		for _, tg := range tags {
			create(&model.RoleTag{RoleID: roleID[code], Tag: tg})
		}
	}

	// ---- Risk command dictionary ----
	type rc struct {
		cmd                string
		prod, staging, dev string
	}
	dict := []rc{
		{"DROP", "high", "mid", "off"}, {"TRUNCATE", "high", "mid", "off"}, {"DELETE", "high", "mid", "off"},
		{"ALTER", "high", "mid", "off"}, {"RENAME", "high", "mid", "off"},
		{"GRANT", "mid", "mid", "off"}, {"REVOKE", "mid", "mid", "off"},
	}
	for _, d := range dict {
		create(&model.RiskCommand{Command: d.cmd, TierCode: "prod", Level: d.prod})
		create(&model.RiskCommand{Command: d.cmd, TierCode: "staging", Level: d.staging})
		create(&model.RiskCommand{Command: d.cmd, TierCode: "dev", Level: d.dev})
	}

	// ---- Webhook + settings ----
	create(&model.WebhookConfig{
		Endpoint: cfg.Webhook.Endpoint, Secret: cfg.Webhook.Secret,
		// 默认推**改动数据的那三类**(增/删/改)。查询量大而价值低,「非数据」里是登录与
		// 配置变更 —— 两者都在设置页里一勾就有。订阅按增删改查匹配,见 service.Dispatch。
		Events: "create,delete,update", RetryMax: cfg.Webhook.RetryMax, Enabled: cfg.Webhook.Enabled,
	})
	settings := map[string]any{
		"gateway.defaultPolicy":   cfg.Gateway.DefaultPolicy,
		"gateway.execTimeout":     cfg.Gateway.ExecTimeoutSeconds,
		"gateway.asyncExecTimeout": 5400, // 后台异步执行上限(秒),默认 90min
		// 审批单执行每条语句的时限(秒),默认 1 小时 —— 与终端的 execTimeout 分开,见 service.approvalExecTimeout
		"gateway.approvalExecTimeout": 3600,

		// 单个导出任务的行数/原始字节上限(0=不限;缺省时代码内置同样的默认值,
		// 所以老库不回填也生效) — service/export.go exportLimits。
		"export.maxRows":  5_000_000,
		"export.maxBytes": 2_000_000_000,
		// 单个导出任务的整体执行超时(秒),默认 30min — service/export.go exportExecTimeout。
		"export.execTimeout": 1800,
		// 导出归档在服务器上保留几天,过期由每小时一次的清理任务删除(0=永久保留)。
		// 这不只是省磁盘:导出文件是生产数据的副本,留着就是一直存在的一份拷贝。
		// 任务行不删 —— 谁导了什么、多少行、什么时候,要比文件活得久。
		"export.retentionDays": 3,

		"approval.onTimeout":      "auto-escalate",
		"approval.timeoutMinutes": 720,
		"security.sessionTTL":     "8h",
		"security.idleLock":       true,
		"security.idleMinutes":    15,
		"security.requireMFA":     true,
		// Off by default: turning it on blocks every PROD operation for anyone not
		// yet enrolled, so it is an explicit rollout decision.
		"security.mfaMandatory":   false,
		// One PROD step-up vouches for a session on that instance for this long.
		"security.mfaGraceMinutes": 30,
		"security.ipAllowlist":    "",
		"notify.larkChannel":      "",

		// 聊天审批卡片,默认关着。老库里没有这些键,读的一侧按同样的默认值(service/chat_runtime.go)。
		"chat.cardLang":       "zh",
		"chat.lark.enabled":   false,
		"chat.lark.region":    "feishu",
		"chat.lark.appId":     "",
		"chat.lark.appSecret": "",
		// Telegram 默认不带语句:卡片上的内容会留在它的服务器上。
		"chat.telegram.enabled":    false,
		"chat.telegram.botToken":   "",
		"chat.telegram.includeSql": false,
		"chat.telegram.proxyToken": "",
	}
	for k, v := range settings {
		b, _ := json.Marshal(v)
		create(&model.Setting{K: k, V: string(b)})
	}
	if seedErr != nil {
		return nil, fmt.Errorf("seed reference data: %w", seedErr)
	}
	return roleID, nil
}

// seedFreshData bootstraps a fresh DB with just the reference data plus a single
// platform-admin account for first login. All business data (users beyond the
// admin, connections, approvals, audit) starts empty so the console reflects real
// usage rather than prototype fixtures. Only ever called on an empty DB (dev).
// Default login: linwei@aegisdb.io / aegisdb123.
func seedFreshData(repo *repository.Repo, cfg *Config) error {
	roleID, err := seedReference(repo, cfg)
	if err != nil {
		return err
	}
	db := repo.DB()
	pw, _ := crypto.HashPassword("aegisdb123")

	admin := model.User{
		Name: "Lin Wei", Initials: "LW", Dept: "DBA 组", Email: "linwei@aegisdb.io",
		RoleID: roleID["admin"], Status: "active", LastActive: "刚刚", PasswordHash: pw,
	}
	if err := db.Create(&admin).Error; err != nil {
		return err
	}
	if err := db.Create(&model.RoleMember{RoleID: roleID["admin"], UserID: admin.ID}).Error; err != nil {
		return err
	}

	slog.Info("seed data inserted", "users", 1, "connections", 0)
	return nil
}

// seedSchema backfills the simulated db.table tree (US#11). Idempotent: it runs
// on every boot but writes only when no schema objects exist yet, so DBs created
// before schema seeding existed self-heal without a wipe.
func seedSchema(repo *repository.Repo) error {
	if repo.Count(&model.SchemaObject{}) > 0 {
		return nil
	}
	conns, err := repo.ListConnections()
	if err != nil {
		return err
	}
	idByName := map[string]int64{}
	for _, c := range conns {
		idByName[c.Name] = c.ID
	}
	schema := map[string]map[string][]string{
		"order-cluster":     {"orders_db": {"orders", "orders_2024_q3", "payments"}},
		"user-cluster":      {"users_db": {"users", "sessions", "profiles"}},
		"analytics-ro":      {"analytics": {"events", "funnels", "retention"}},
		"order-cluster-stg": {"orders_db": {"orders", "orders_2024_q3"}},
		"sandbox-dev":       {"sandbox": {"scratch", "tmp_import"}},
	}
	db := repo.DB()
	for connName, dbs := range schema {
		id, ok := idByName[connName]
		if !ok {
			continue
		}
		for dbName, tables := range dbs {
			for _, tbl := range tables {
				if err := db.Create(&model.SchemaObject{ConnectionID: id, Database: dbName, Tbl: tbl}).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}
