package bootstrap

import (
	"fmt"
	"log/slog"
	"strings"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// InitDatabase seeds ONLY the reference data (roles, menus, capabilities, risk
// dictionary, settings) — no demo users/connections — and creates the
// platform-admin account. The schema must already be migrated by the caller
// (see bootstrap.Migrate). Safe to re-run: reference data is seeded only on an
// empty DB and the admin is upserted.
func InitDatabase(repo *repository.Repo, cfg *Config, adminEmail, adminPassword, adminName string) error {
	// reference data (idempotent: only on an empty roles table)
	if repo.Count(&model.Role{}) == 0 {
		if _, err := seedReference(repo, cfg); err != nil {
			return fmt.Errorf("seed reference data: %w", err)
		}
		slog.Info("reference data seeded (roles, menus, capabilities, dictionary, settings)")
	} else {
		slog.Info("reference data already present — skipped")
	}
	// GLI (灰度) env rows are backfilled idempotently so existing prod DBs self-heal.
	if err := seedGliEnv(repo); err != nil {
		return fmt.Errorf("seed gli env: %w", err)
	}
	// Tier/environment rows, likewise idempotent: an install with none cannot
	// resolve any environment to its tier and would refuse every connection edit.
	if err := backfillEnvTiers(repo.DB()); err != nil {
		return fmt.Errorf("seed env tiers: %w", err)
	}
	// admin account
	u, err := UpsertAdmin(repo, adminEmail, adminPassword, adminName)
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	slog.Info("platform admin ready", "email", u.Email, "name", u.Name)
	return nil
}

// UpsertAdmin creates (or resets the password of) the platform-admin user with
// the given credentials, assigning it the built-in "admin" role.
func UpsertAdmin(repo *repository.Repo, email, password, name string) (*model.User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil, fmt.Errorf("admin email required")
	}
	if err := validateAdminPassword(password); err != nil {
		return nil, err
	}
	role, err := repo.GetRoleByCode("admin")
	if err != nil || role == nil {
		return nil, fmt.Errorf("admin role not found — run reference seeding first")
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = strings.SplitN(email, "@", 2)[0]
	}

	db := repo.DB()
	var u model.User
	if err := db.Where("email = ?", email).First(&u).Error; err == nil {
		// existing account: reset password + ensure admin role/active
		u.PasswordHash = hash
		u.RoleID = role.ID
		u.Status = "active"
		if err := db.Save(&u).Error; err != nil {
			return nil, err
		}
	} else {
		u = model.User{
			Name: name, Email: email, RoleID: role.ID, Status: "active",
			Initials: abbrev(name), Dept: "—", LastActive: "—", PasswordHash: hash,
		}
		if err := db.Create(&u).Error; err != nil {
			return nil, err
		}
	}
	// membership (so the user shows under the admin role's member list)
	_ = repo.AddMember(role.ID, u.ID)
	return &u, nil
}

// validateAdminPassword enforces a stronger policy for the sole super-admin
// account: at least 12 characters spanning ≥3 of {lower, upper, digit, symbol}.
func validateAdminPassword(pw string) error {
	if len(pw) < 12 {
		return fmt.Errorf("管理员口令至少 12 位(当前 %d 位)", len(pw))
	}
	var hasLower, hasUpper, hasDigit, hasSymbol bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	classes := 0
	for _, ok := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if ok {
			classes++
		}
	}
	if classes < 3 {
		return fmt.Errorf("管理员口令需包含大小写字母、数字、符号中的至少 3 类")
	}
	return nil
}

// abbrev builds a 2-char avatar label from a display name.
func abbrev(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "AD"
	}
	if parts := strings.Fields(name); len(parts) >= 2 {
		return strings.ToUpper(string([]rune(parts[0])[:1]) + string([]rune(parts[1])[:1]))
	}
	r := []rune(name)
	if len(r) >= 2 {
		return strings.ToUpper(string(r[:2]))
	}
	return strings.ToUpper(string(r))
}
