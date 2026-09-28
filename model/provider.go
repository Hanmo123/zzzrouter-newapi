package model

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Provider is the commercial operator of a channel, NOT the model's
// Vendor (e.g. OpenAI) or the channel's protocol Type (e.g. OpenAI-compatible).
// Credentials and commercial terms belong to individual channels.
// Soft deletion preserves provider identities for future evaluation history.
type Provider struct {
	ID                   int            `json:"id"`
	Code                 string         `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Name                 string         `json:"name" gorm:"size:128;not null;index"`
	Description          string         `json:"description" gorm:"type:text"`
	SkipDegradationCheck bool           `json:"skip_degradation_check"`
	CreatedTime          int64          `json:"created_time" gorm:"bigint"`
	UpdatedTime          int64          `json:"updated_time" gorm:"bigint"`
	DeletedAt            gorm.DeletedAt `json:"-" gorm:"index"`
	ChannelCount         int64          `json:"channel_count" gorm:"-"`
}

// migrateChannelProviders runs after AutoMigrate. Legacy clients that do
// not send provider_id use this same real provider rather than an orphan ID 0.
func migrateChannelProviders(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		provider, err := defaultProvider(tx)
		if err != nil {
			return err
		}
		return tx.Model(&Channel{}).Where("provider_id IS NULL OR provider_id = 0").Update("provider_id", provider.ID).Error
	})
}

func defaultProvider(tx *gorm.DB) (*Provider, error) {
	var provider Provider
	err := tx.Where("code = ?", "default").First(&provider).Error
	if err == nil {
		return &provider, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	provider = Provider{Code: "default", Name: "Default provider", CreatedTime: common.GetTimestamp(), UpdatedTime: common.GetTimestamp()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&provider).Error; err != nil {
		return nil, err
	}
	err = tx.Where("code = ?", "default").First(&provider).Error
	return &provider, err
}

// validateChannelProvider holds the provider row lock until the channel
// write commits, serializing assignment against provider deletion. On patch,
// omitted fields stay omitted and GORM preserves their persisted values.
func validateChannelProvider(tx *gorm.DB, channel *Channel, create bool) error {
	if channel.ProviderID < 0 {
		return errors.New("provider_id must be positive")
	}
	if create && channel.ProviderID == 0 {
		provider, err := defaultProvider(tx)
		if err != nil {
			return err
		}
		channel.ProviderID = provider.ID
	}
	if channel.UpstreamRatio != nil {
		ratio := *channel.UpstreamRatio
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
			return errors.New("upstream_ratio must be a finite positive number")
		}
	} else if create {
		ratio := 1.0
		channel.UpstreamRatio = &ratio
	}
	if channel.ProviderID == 0 {
		return nil
	}
	var provider Provider
	if err := lockForUpdate(tx).First(&provider, channel.ProviderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("provider does not exist")
		}
		return err
	}
	return nil
}

func (provider *Provider) Save(skipDegradation ...*bool) error {
	provider.Name = strings.TrimSpace(provider.Name)
	provider.Description = strings.TrimSpace(provider.Description)
	if provider.Name == "" || utf8.RuneCountInString(provider.Name) > 128 {
		return errors.New("provider name must contain 1 to 128 characters")
	}
	if utf8.RuneCountInString(provider.Description) > 4096 {
		return errors.New("provider description must not exceed 4096 characters")
	}
	if provider.ID < 0 {
		return errors.New("invalid provider ID")
	}
	provider.UpdatedTime = common.GetTimestamp()
	if provider.ID == 0 {
		provider.Code = uuid.NewString()
		provider.CreatedTime = provider.UpdatedTime
		return DB.Create(provider).Error
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var saved Provider
		if err := lockForUpdate(tx).First(&saved, provider.ID).Error; err != nil {
			return err
		}
		provider.Code, provider.CreatedTime = saved.Code, saved.CreatedTime
		provider.SkipDegradationCheck = saved.SkipDegradationCheck
		if len(skipDegradation) > 0 && skipDegradation[0] != nil {
			provider.SkipDegradationCheck = *skipDegradation[0]
		}
		return tx.Model(&saved).Updates(map[string]any{"name": provider.Name, "description": provider.Description, "skip_degradation_check": provider.SkipDegradationCheck, "updated_time": provider.UpdatedTime}).Error
	})
}

func DeleteProvider(id int) error {
	if id <= 0 {
		return errors.New("invalid provider ID")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var provider Provider
		if err := lockForUpdate(tx).First(&provider, id).Error; err != nil {
			return err
		}
		if provider.Code == "default" {
			return errors.New("the default provider cannot be deleted")
		}
		var count int64
		if err := tx.Model(&Channel{}).Where("provider_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errors.New("reassign or delete this provider's channels first")
		}
		return tx.Delete(&provider).Error
	})
}

func ListProviders(keyword string, offset, limit int) ([]Provider, int64, error) {
	query := DB.Model(&Provider{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("name LIKE ?", "%"+keyword+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	providers := make([]Provider, 0)
	if err := query.Order("id ASC").Offset(offset).Limit(limit).Find(&providers).Error; err != nil {
		return nil, 0, err
	}
	ids := make([]int, len(providers))
	for i := range providers {
		ids[i] = providers[i].ID
	}
	var counts []struct {
		ProviderID int
		Count      int64
	}
	if len(ids) > 0 {
		if err := DB.Model(&Channel{}).Select("provider_id, COUNT(*) AS count").Where("provider_id IN ?", ids).Group("provider_id").Scan(&counts).Error; err != nil {
			return nil, 0, err
		}
	}
	byID := make(map[int]int64, len(counts))
	for _, count := range counts {
		byID[count.ProviderID] = count.Count
	}
	for i := range providers {
		providers[i].ChannelCount = byID[providers[i].ID]
	}
	return providers, total, nil
}
