package repository

import (
	"time"

	"velagateway/internal/model"
)

// ----------------------------------------------------- 开放接口客户端

func (r *Repo) ListAPIClients() ([]model.APIClient, error) {
	var cs []model.APIClient
	err := r.db.Order("id asc").Find(&cs).Error
	return cs, err
}

func (r *Repo) GetAPIClient(id int64) (*model.APIClient, error) {
	var c model.APIClient
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetAPIClientByKey looks a client up by the PUBLIC half of its credential. The
// secret is never queried — it is compared against the stored bcrypt hash by the
// caller, so a timing difference here reveals only whether a key exists.
func (r *Repo) GetAPIClientByKey(key string) (*model.APIClient, error) {
	var c model.APIClient
	if err := r.db.Where("`key` = ?", key).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repo) CreateAPIClient(c *model.APIClient) error { return r.db.Create(c).Error }

func (r *Repo) UpdateAPIClientFields(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.APIClient{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repo) DeleteAPIClient(id int64) error {
	return r.db.Delete(&model.APIClient{}, id).Error
}

// TouchAPIClient records that the credential was just used, so an operator can
// see which integrations are live and which are dead credentials to revoke.
// Failures are the caller's to ignore: this must never fail a request.
func (r *Repo) TouchAPIClient(id int64, at time.Time) error {
	return r.db.Model(&model.APIClient{}).Where("id = ?", id).
		Update("last_used_at", at).Error
}

// GetReleaseByIdemKey finds the release a previous, identical API call created.
func (r *Repo) GetReleaseByIdemKey(key string) (*model.Release, error) {
	var rel model.Release
	if err := r.db.Where("idem_key = ?", key).First(&rel).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}
