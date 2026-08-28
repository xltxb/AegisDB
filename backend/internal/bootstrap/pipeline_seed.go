package bootstrap

import (
	"log/slog"

	"gorm.io/gorm"

	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// Reference data for the two features that ship together: the 规范审查规则库 and
// the 发布流程 (CI/CD) templates.
//
// All three functions here run on EVERY boot and every `migrate`, and all three
// are idempotent, because a production upgrade runs `migrate` while `seed` is a
// first-install-only step. Reference data a later release introduces has to be
// backfilled on the migrate path or the feature ships dead: a menu key with no
// RoleMenu row reads as denied for everyone (including administrators), and a
// review library with no rows passes every script.

// backfillPipelineMenu grants the new "pipeline" menu to whoever already holds
// "terminal".
//
// Terminal is the right ancestor: a release ends in an execution against an
// instance, and the people who may execute there are exactly the ones who should
// be able to raise one. It is NOT inherited from "approve" — approving a release
// and raising one are different acts, and the approve stage still routes through
// the ordinary approval chain regardless of this grant.
//
// Written only when the key is entirely absent, so a deliberate revocation is
// not undone on the next restart.
func backfillPipelineMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", "pipeline").Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var terminal []model.RoleMenu
	if err := db.Where("menu_key = ?", "terminal").Find(&terminal).Error; err != nil {
		return err
	}
	for _, r := range terminal {
		if err := db.Create(&model.RoleMenu{
			RoleID: r.RoleID, MenuKey: "pipeline", Enabled: r.Enabled,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedReviewLibrary inserts the shipped review rules that are not in the table
// yet. Existing rows are never touched — see repository.SeedSQLReviewRules for
// why an upgrade must not reset a level an operator deliberately lowered.
func seedReviewLibrary(db *gorm.DB) error {
	n, err := repository.New(db).SeedSQLReviewRules()
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Info("seeded SQL review rules", "added", n)
	}
	return nil
}

// defaultPipelines are the two flows a fresh install starts with.
//
// The standard flow is deliberately conservative: review, then a human, then a
// backup point, then execution, then a verification and a notification. The dev
// flow exists so the feature is usable on day one without an approver in the
// loop — it is scoped to the dev TIER, so it cannot be selected for production.
//
// Both are ordinary rows: an administrator edits, reorders or deletes them, and
// nothing here recreates a template that was deliberately removed (the seed only
// runs when the table is empty).
type seedStage struct {
	name, typ, config string
}

var defaultPipelines = []struct {
	name, desc, tier string
	isDefault        bool
	stages           []seedStage
}{
	{
		name: "标准发布流程", desc: "规范审查 → 人工审批 → 备份点 → 执行 → 校验 → 通知", tier: "", isDefault: true,
		stages: []seedStage{
			{"规范审查", model.StageReview, `{"failOn":"error"}`},
			{"人工审批", model.StageApprove, `{}`},
			{"备份/回滚点", model.StageBackup, `{"sql":""}`},
			{"执行变更", model.StageExecute, `{}`},
			{"执行后校验", model.StageVerify, `{"sql":""}`},
			{"结果通知", model.StageNotify, `{}`},
		},
	},
	{
		name: "开发自助发布", desc: "仅规范审查后直接执行,限开发分层", tier: model.EnvDev, isDefault: false,
		stages: []seedStage{
			{"规范审查", model.StageReview, `{"failOn":"error"}`},
			{"执行变更", model.StageExecute, `{}`},
		},
	},
}

// seedDefaultPipelines creates the starter templates on an empty table.
func seedDefaultPipelines(db *gorm.DB) error {
	var n int64
	if err := db.Model(&model.Pipeline{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, p := range defaultPipelines {
		row := &model.Pipeline{
			Name: p.name, Description: p.desc, TierCode: p.tier,
			Enabled: true, IsDefault: p.isDefault,
		}
		if err := db.Create(row).Error; err != nil {
			return err
		}
		for i, st := range p.stages {
			// 执行阶段固定 abort:失败后继续,后面的校验与通知会宣告一次并未发生的变更成功。
			onFail := model.OnFailureAbort
			if err := db.Create(&model.PipelineStage{
				PipelineID: row.ID, StepOrder: i + 1, Name: st.name, Type: st.typ,
				Config: st.config, OnFailure: onFail,
			}).Error; err != nil {
				return err
			}
		}
	}
	slog.Info("seeded default release pipelines", "count", len(defaultPipelines))
	return nil
}

// seedPipelineReference runs all three backfills. Both schema paths call it —
// the SQL-migration path (MySQL/production) and the AutoMigrate path (dev,
// tests) — because a backfill that only ran on one of them is a feature that
// works in development and is invisible in production.
func seedPipelineReference(db *gorm.DB) error {
	if err := backfillPipelineMenu(db); err != nil {
		return err
	}
	if err := backfillProjectMenu(db); err != nil {
		return err
	}
	if err := seedReviewLibrary(db); err != nil {
		return err
	}
	return seedDefaultPipelines(db)
}
