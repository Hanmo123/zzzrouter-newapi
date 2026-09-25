package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PublicProvider and PublicChannel are explicit public projections. Never embed
// Provider/Channel or serialize an admin record here: upstream prices, secrets,
// internal descriptions, settings and future admin fields must stay private.
type PublicProvider struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ChannelCount int64  `json:"channel_count"`
}

type PublicChannel struct {
	ID            int     `json:"id"`
	ProviderID    int     `json:"provider_id"`
	ProviderName  string  `json:"provider_name"`
	Name          string  `json:"name"`
	Type          int     `json:"type"`
	Models        string  `json:"models"`
	PublicRatio   float64 `json:"public_ratio"`
	IsDegraded    *bool   `json:"is_degraded"`
	IsOnline      *bool   `json:"is_online"`
	TTFTMs        *int64  `json:"ttft_ms"`
	LastCheckedAt *int64  `json:"last_checked_at"`
}

// Catalog filters are intentionally independent from administrative queries.
// Unknown observations do not satisfy the default online/not-degraded filters.
type PublicCatalogQuery struct {
	Page       int
	PageSize   int
	ProviderID int
	IsOnline   string
	IsDegraded string
	SortBy     string
	SortOrder  string
}

func (query *PublicCatalogQuery) Normalize() error {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 20
	}
	if query.Page < 1 || query.Page > 10000 {
		return errors.New("p must be an integer between 1 and 10000")
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		return errors.New("page_size must be an integer between 1 and 100")
	}
	if query.ProviderID < 0 {
		return errors.New("provider_id must be a positive integer")
	}
	if query.IsOnline == "" {
		query.IsOnline = "true"
	}
	if query.IsDegraded == "" {
		query.IsDegraded = "false"
	}
	for _, filter := range []string{query.IsOnline, query.IsDegraded} {
		switch filter {
		case "true", "false", "all", "unknown":
		default:
			return errors.New("is_online and is_degraded must be true, false, all or unknown")
		}
	}
	if query.SortBy == "" {
		query.SortBy = "public_ratio"
	}
	if query.SortBy == "ttft" {
		query.SortBy = "ttft_ms"
	}
	if query.SortBy != "public_ratio" && query.SortBy != "ttft_ms" {
		return errors.New("sort_by must be public_ratio, ttft or ttft_ms")
	}
	query.SortOrder = strings.ToLower(query.SortOrder)
	if query.SortOrder == "" {
		query.SortOrder = "asc"
	}
	if query.SortOrder != "asc" && query.SortOrder != "desc" {
		return errors.New("sort_order must be asc or desc")
	}
	return nil
}

// publicCatalogChannels is shared by listing, provider grouping and counts so
// filters cannot leak counts from disabled/orphaned/deleted-provider channels.
// The provider subquery follows GORM's soft-delete scope and table naming.
func publicCatalogChannels(query PublicCatalogQuery) *gorm.DB {
	providers := DB.Model(&Provider{}).Select("id, name")
	channels := DB.Model(&Channel{}).
		Table(DB.NamingStrategy.TableName("Channel")+" AS c").
		Joins("JOIN (?) AS provider ON provider.id = c.provider_id", providers).
		Where("c.status = ?", common.ChannelStatusEnabled)
	if query.ProviderID > 0 {
		channels = channels.Where("c.provider_id = ?", query.ProviderID)
	}
	for _, filter := range []struct{ column, value string }{
		{"c.is_online", query.IsOnline},
		{"c.is_degraded", query.IsDegraded},
	} {
		switch filter.value {
		case "true", "false":
			channels = channels.Where(filter.column+" = ?", filter.value == "true")
		case "unknown":
			channels = channels.Where(filter.column + " IS NULL")
		}
	}
	return channels
}

func ListPublicProviders(query PublicCatalogQuery) ([]PublicProvider, int64, error) {
	if err := query.Normalize(); err != nil {
		return nil, 0, err
	}
	channels := publicCatalogChannels(query)
	visibleIDs := channels.Session(&gorm.Session{}).Select("provider.id").Group("provider.id")
	var total int64
	if err := DB.Table("(?) AS visible_providers", visibleIDs).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	providers := make([]PublicProvider, 0)
	err := channels.Select("provider.id, provider.name, COUNT(c.id) AS channel_count").
		Group("provider.id, provider.name").Order("provider.id ASC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Scan(&providers).Error
	return providers, total, err
}

func ListPublicChannels(query PublicCatalogQuery) ([]PublicChannel, int64, error) {
	if err := query.Normalize(); err != nil {
		return nil, 0, err
	}
	channels := publicCatalogChannels(query)
	var total int64
	if err := channels.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// This SELECT allowlist is a second boundary in addition to the response DTO:
	// no upstream_ratio, keys, URLs, mappings, settings or other private metadata
	// are even loaded. Do not use Select("*") or a Channel destination here.
	channels = channels.Select("c.id, c.provider_id, provider.name AS provider_name, c.name, c.type, c.models, c.public_ratio, c.is_degraded, c.is_online, c.ttft_ms, c.last_checked_at")
	if query.SortBy == "ttft_ms" {
		// SQL NULL ordering differs by dialect; unknown TTFT is always last.
		channels = channels.Order("CASE WHEN c.ttft_ms IS NULL THEN 1 ELSE 0 END ASC")
	}
	channels = channels.Order(clause.OrderByColumn{
		Column: clause.Column{Table: "c", Name: query.SortBy}, Desc: query.SortOrder == "desc",
	}).Order(clause.OrderByColumn{Column: clause.Column{Table: "c", Name: "id"}})
	items := make([]PublicChannel, 0)
	err := channels.Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Scan(&items).Error
	return items, total, err
}
