package model

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
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
	ID            int              `json:"id"`
	ProviderID    int              `json:"provider_id"`
	ProviderName  string           `json:"provider_name"`
	Name          string           `json:"name"`
	Type          int              `json:"type"`
	Model         string           `json:"model"`
	PublicRatio   *ModelRatioRange `json:"public_ratio"`
	IsDegraded    *bool            `json:"is_degraded"`
	IsOnline      *bool            `json:"is_online"`
	TTFTMs        *int64           `json:"ttft_ms"`
	LastCheckedAt *int64           `json:"last_checked_at"`
}

// Catalog filters are intentionally independent from administrative queries.
// Unknown observations do not satisfy the default online/not-degraded filters.
type PublicCatalogQuery struct {
	Page       int
	PageSize   int
	ProviderID int
	Model      string
	Search     string
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
	if len(query.Model) > 255 || strings.TrimSpace(query.Model) != query.Model || strings.Contains(query.Model, ",") || strings.ContainsFunc(query.Model, unicode.IsControl) {
		return errors.New("model must be one configured model name of at most 255 bytes")
	}
	if len(query.Search) > 128 || strings.TrimSpace(query.Search) != query.Search || strings.ContainsFunc(query.Search, unicode.IsControl) {
		return errors.New("search must be at most 128 bytes without surrounding whitespace")
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
	if query.Model != "" {
		// Match a complete comma-delimited model, never a substring or LIKE
		// wildcard. Normalize whitespace around delimiters, not inside names:
		// "model a" must never match "modela". Forms use at most one space;
		// repeating also covers common hand-edited lists with extra spaces.
		models := "c.models"
		for range 4 {
			models = "REPLACE(REPLACE(" + models + ", ', ', ','), ' ,', ',')"
		}
		name := "," + query.Model + ","
		switch common.MainDatabaseType() {
		case common.DatabaseTypeMySQL:
			channels = channels.Where("LOCATE(BINARY ?, BINARY CONCAT(',', "+models+", ',')) > 0", name)
		case common.DatabaseTypePostgreSQL:
			channels = channels.Where("POSITION(? IN (',' || "+models+" || ',')) > 0", name)
		default:
			channels = channels.Where("INSTR(',' || "+models+" || ',', ?) > 0", name)
		}
	}
	return channels
}

// ListPublicModels returns only models on channels visible under the same
// status/provider conditions as the directory. No private channel columns are
// selected, and pagination happens after deduplicating model tokens.
func ListPublicModels(query PublicCatalogQuery) ([]string, int64, error) {
	if err := query.Normalize(); err != nil {
		return nil, 0, err
	}
	rows, err := publicModelRows(query)
	if err != nil {
		return nil, 0, err
	}
	seen := make(map[string]struct{})
	search := strings.ToLower(query.Search)
	for _, row := range rows {
		name := row.Model
		if search == "" || strings.Contains(strings.ToLower(name), search) {
			seen[name] = struct{}{}
		}
	}
	models := make([]string, 0, len(seen))
	for name := range seen {
		models = append(models, name)
	}
	slices.Sort(models)
	total := int64(len(models))
	start := min((query.Page-1)*query.PageSize, len(models))
	return models[start:min(start+query.PageSize, len(models))], total, nil
}

// publicModelRows projects channel-model records before filtering, sorting and
// pagination. It never serializes the config map (which includes private cost).
func publicModelRows(query PublicCatalogQuery) ([]PublicChannel, error) {
	var channels []struct {
		ID           int
		ProviderID   int
		ProviderName string
		Name         string
		Type         int
		Models       string
		ModelConfigs ChannelModelConfigs `gorm:"type:text"`
	}
	err := publicCatalogChannels(query).Select("c.id, c.provider_id, provider.name AS provider_name, c.name, c.type, c.models, c.model_configs").Scan(&channels).Error
	if err != nil {
		return nil, err
	}
	items := make([]PublicChannel, 0)
	for _, channel := range channels {
		seen := make(map[string]bool)
		for raw := range strings.SplitSeq(channel.Models, ",") {
			name := strings.TrimSpace(raw)
			if name == "" || seen[name] || (query.Model != "" && name != query.Model) {
				continue
			}
			seen[name] = true
			config := channel.ModelConfigs[name]
			if !matchesPublicObservation(config.IsOnline, query.IsOnline) || !matchesPublicObservation(config.IsDegraded, query.IsDegraded) {
				continue
			}
			items = append(items, PublicChannel{ID: channel.ID, ProviderID: channel.ProviderID, ProviderName: channel.ProviderName, Name: channel.Name, Type: channel.Type, Model: name, PublicRatio: config.ModelPublicRatio(name), IsOnline: config.IsOnline, IsDegraded: config.IsDegraded, TTFTMs: config.TTFTMs, LastCheckedAt: config.LastCheckedAt})
		}
	}
	return items, nil
}

func matchesPublicObservation(value *bool, filter string) bool {
	switch filter {
	case "true":
		return value != nil && *value
	case "false":
		return value != nil && !*value
	case "unknown":
		return value == nil
	default:
		return true
	}
}

func ListPublicProviders(query PublicCatalogQuery) ([]PublicProvider, int64, error) {
	if err := query.Normalize(); err != nil {
		return nil, 0, err
	}
	rows, err := publicModelRows(query)
	if err != nil {
		return nil, 0, err
	}
	byProvider := make(map[int]*PublicProvider)
	seen := make(map[int]bool)
	for _, row := range rows {
		if seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		if byProvider[row.ProviderID] == nil {
			byProvider[row.ProviderID] = &PublicProvider{ID: row.ProviderID, Name: row.ProviderName}
		}
		byProvider[row.ProviderID].ChannelCount++
	}
	items := make([]PublicProvider, 0, len(byProvider))
	for _, item := range byProvider {
		items = append(items, *item)
	}
	slices.SortFunc(items, func(a, b PublicProvider) int { return cmp.Compare(a.ID, b.ID) })
	start := min((query.Page-1)*query.PageSize, len(items))
	return items[start:min(start+query.PageSize, len(items))], int64(len(items)), nil
}

func ListPublicChannels(query PublicCatalogQuery) ([]PublicChannel, int64, error) {
	if err := query.Normalize(); err != nil {
		return nil, 0, err
	}
	items, err := publicModelRows(query)
	if err != nil {
		return nil, 0, err
	}
	slices.SortFunc(items, func(a, b PublicChannel) int {
		result := 0
		if query.SortBy == "ttft_ms" {
			if a.TTFTMs == nil && b.TTFTMs != nil {
				return 1
			}
			if a.TTFTMs != nil && b.TTFTMs == nil {
				return -1
			}
			if a.TTFTMs != nil && b.TTFTMs != nil {
				result = cmp.Compare(*a.TTFTMs, *b.TTFTMs)
			}
		} else {
			if a.PublicRatio == nil && b.PublicRatio != nil {
				return 1
			}
			if a.PublicRatio != nil && b.PublicRatio == nil {
				return -1
			}
			if a.PublicRatio != nil && b.PublicRatio != nil {
				result = cmp.Compare(a.PublicRatio.Max, b.PublicRatio.Max)
				if result == 0 {
					result = cmp.Compare(a.PublicRatio.Min, b.PublicRatio.Min)
				}
			}
		}
		if query.SortOrder == "desc" {
			result = -result
		}
		if result == 0 {
			result = cmp.Compare(a.ID, b.ID)
		}
		if result == 0 {
			result = strings.Compare(a.Model, b.Model)
		}
		return result
	})
	start := min((query.Page-1)*query.PageSize, len(items))
	return items[start:min(start+query.PageSize, len(items))], int64(len(items)), nil
}
