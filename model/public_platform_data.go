package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

const maxPublicStatisticsInteger int64 = 9_007_199_254_740_991

type PublicPlatformObservation struct {
	State     string `json:"state"`
	StartedAt int64  `json:"started_at"`
	TTFTMs    *int64 `json:"ttft_ms"`
}

type PublicPlatformStatistics struct {
	AsOf           int64                      `json:"as_of"`
	Providers      int64                      `json:"providers"`
	Channels       int64                      `json:"channels"`
	Models         int64                      `json:"models"`
	Evaluations24h int64                      `json:"evaluations_24h"`
	Latest         *PublicPlatformObservation `json:"latest"`
}

// Counts use the same public, enabled channel scope as the directory. Probe
// observations must match both the current channel instance and its provider.
func GetPublicPlatformStatistics(ctx context.Context, now time.Time) (PublicPlatformStatistics, error) {
	result := PublicPlatformStatistics{AsOf: now.UnixMilli()}
	query := PublicCatalogQuery{IsOnline: "all", IsDegraded: "all"}
	if err := query.Normalize(); err != nil {
		return result, err
	}
	channels := publicCatalogChannels(query).WithContext(ctx)
	var counts struct {
		Providers int64
		Channels  int64
	}
	if err := channels.Session(&gorm.Session{}).Select("COUNT(*) AS channels, COUNT(DISTINCT c.provider_id) AS providers").Scan(&counts).Error; err != nil {
		return result, err
	}
	result.Providers, result.Channels = counts.Providers, counts.Channels
	if _, total, err := ListPublicModels(query); err != nil {
		return result, err
	} else {
		result.Models = total
	}
	visible := channels.Session(&gorm.Session{}).Select("c.instance_id, c.provider_id")
	samples := DB.WithContext(ctx).Model(&ChannelDegradationSample{}).
		Table(DB.NamingStrategy.TableName("ChannelDegradationSample")+" AS sample").
		Joins("JOIN (?) AS visible ON visible.instance_id = sample.channel_instance_id AND visible.provider_id = sample.provider_id", visible).
		Where("sample.started_at <= ? AND sample.completed_at <= ?", now.UnixMilli(), now.UnixMilli())
	if err := samples.Session(&gorm.Session{}).Where("sample.started_at >= ?", now.Add(-24*time.Hour).UnixMilli()).Count(&result.Evaluations24h).Error; err != nil {
		return result, err
	}
	var recent []PublicPlatformObservation
	unknown := "COALESCE(sample.error_code, '') IN ('cancelled', 'invalid_target', 'invalid_format', 'invalid_proxy', 'invalid_request')"
	if err := samples.Session(&gorm.Session{}).
		Select("CASE WHEN sample.state = 'error' AND (" + unknown + ") THEN 'unknown' ELSE sample.state END AS state, sample.started_at, sample.ttft_ms").
		Order("sample.started_at DESC, sample.id DESC").Limit(1).Scan(&recent).Error; err != nil {
		return result, err
	}
	if len(recent) != 0 {
		result.Latest = &recent[0]
	}
	return result, nil
}

// Read existing consumption facts from the configured log database. Neither
// manual display rows nor quotas/prices are used to infer token quantities.
func ListPublicConsumptionDailyUsage(ctx context.Context, days int, now time.Time) (PublicDailyUsageSeries, error) {
	if days != 7 && days != 30 && days != 90 && days != 365 {
		return PublicDailyUsageSeries{}, ErrInvalidDailyUsage
	}
	end := now.UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -days)
	response := PublicDailyUsageSeries{Unit: "tokens", Source: "consumption_logs", Timezone: "UTC", From: start.Format("2006-01-02"), To: end.AddDate(0, 0, -1).Format("2006-01-02"), Days: days, Items: make([]DailyUsagePoint, 0, days)}
	if LOG_DB == nil {
		return response, errors.New("consumption log store is unavailable")
	}
	bucket := "(created_at / 86400) * 86400"
	if LOG_DB.Dialector.Name() == "mysql" {
		bucket = "FLOOR(created_at / 86400) * 86400"
	}
	if LOG_DB.Dialector.Name() == "clickhouse" {
		bucket = "intDiv(created_at, 86400) * 86400"
	}
	tokens := "CAST(prompt_tokens AS BIGINT) + CAST(completion_tokens AS BIGINT)"
	if LOG_DB.Dialector.Name() == "mysql" {
		tokens = "CAST(prompt_tokens AS SIGNED) + CAST(completion_tokens AS SIGNED)"
	}
	var rows []struct {
		StartedAt  int64
		TokenCount int64
	}
	err := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("type = ? AND created_at >= ? AND created_at < ? AND prompt_tokens >= 0 AND completion_tokens >= 0", LogTypeConsume, start.Unix(), end.Unix()).
		Select(bucket + " AS started_at, SUM(" + tokens + ") AS token_count").
		Group(bucket).Order("started_at ASC").Scan(&rows).Error
	if err != nil {
		return response, err
	}
	byDate := make(map[int64]int64, len(rows))
	for _, row := range rows {
		if row.TokenCount < 0 || row.TokenCount > maxPublicStatisticsInteger || response.Total > maxPublicStatisticsInteger-row.TokenCount {
			return response, errors.New("token total exceeds safe public integer range")
		}
		byDate[row.StartedAt] = row.TokenCount
		response.Total += row.TokenCount
		response.RecordedDays++
	}
	for i := range days {
		date := start.AddDate(0, 0, i)
		point := DailyUsagePoint{Date: date.Format("2006-01-02")}
		if count, exists := byDate[date.Unix()]; exists {
			point.TokenCount = &count
		}
		response.Items = append(response.Items, point)
	}
	return response, nil
}
