package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxPublicDailyUsageCount int64 = 1_000_000_000_000
const MaxPublicDailyUsageBatch = 366

var ErrInvalidDailyUsage = errors.New("invalid daily usage")

// Manually maintained, platform-wide display data, not consumption logs or
// billing facts. Values are daily tokens, not request counts. Retain the legacy
// request_count column and manually entered numbers without unit conversion;
// no measured request logs are being relabeled or used to infer token usage.
type PublicDailyUsage struct {
	Day        string `gorm:"size:10;primaryKey"`
	TokenCount int64  `gorm:"column:request_count;type:bigint;not null"`
	UpdatedAt  int64  `gorm:"type:bigint;not null"`
	UpdatedBy  int    `gorm:"not null"`
}

type DailyUsageInput struct {
	Date       string `json:"date"`
	TokenCount *int64 `json:"token_count"`
}

type DailyUsagePoint struct {
	Date       string `json:"date"`
	TokenCount *int64 `json:"token_count"`
}

type PublicDailyUsageSeries struct {
	Unit         string            `json:"unit"`
	Timezone     string            `json:"timezone"`
	Source       string            `json:"source"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	Days         int               `json:"days"`
	Total        int64             `json:"total"`
	RecordedDays int               `json:"recorded_days"`
	Items        []DailyUsagePoint `json:"items"`
}

func ValidateDailyUsageDate(day string, now time.Time) error {
	date, err := time.Parse("2006-01-02", day)
	if err != nil || date.Year() < 1970 || date.After(now.UTC().Truncate(24*time.Hour)) {
		return fmt.Errorf("%w: date must be YYYY-MM-DD between 1970-01-01 and today (UTC)", ErrInvalidDailyUsage)
	}
	return nil
}

// Validate the whole batch before opening a transaction; duplicate days cannot
// silently overwrite one another. Batching keeps SQLite parameter counts small.
func SetPublicDailyUsage(ctx context.Context, items []DailyUsageInput, editorID int, now time.Time) error {
	if len(items) == 0 || len(items) > MaxPublicDailyUsageBatch || editorID <= 0 {
		return fmt.Errorf("%w: provide 1..366 dates and a valid editor", ErrInvalidDailyUsage)
	}
	seen := make(map[string]bool, len(items))
	records := make([]PublicDailyUsage, 0, len(items))
	for _, item := range items {
		if err := ValidateDailyUsageDate(item.Date, now); err != nil {
			return err
		}
		if seen[item.Date] || item.TokenCount == nil || *item.TokenCount < 0 || *item.TokenCount > MaxPublicDailyUsageCount {
			return fmt.Errorf("%w: dates must be unique and counts must be integers from 0 to 1000000000000", ErrInvalidDailyUsage)
		}
		seen[item.Date] = true
		records = append(records, PublicDailyUsage{Day: item.Date, TokenCount: *item.TokenCount, UpdatedAt: now.Unix(), UpdatedBy: editorID})
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "day"}},
			DoUpdates: clause.AssignmentColumns([]string{"request_count", "updated_at", "updated_by"}),
		}).CreateInBatches(&records, 100).Error
	})
}

// includeToday is a server-side policy, not a public query parameter. Root
// management can maintain today's count; the public page currently ends yesterday.
func ListPublicDailyUsage(ctx context.Context, days int, includeToday bool, now time.Time) (PublicDailyUsageSeries, error) {
	if days != 7 && days != 30 && days != 90 && days != 365 {
		return PublicDailyUsageSeries{}, fmt.Errorf("%w: days must be 7, 30, 90 or 365", ErrInvalidDailyUsage)
	}
	end := now.UTC().Truncate(24 * time.Hour)
	if !includeToday {
		end = end.AddDate(0, 0, -1)
	}
	start := end.AddDate(0, 0, -(days - 1))
	response := PublicDailyUsageSeries{
		Unit: "tokens", Timezone: "UTC", Source: "manual", From: start.Format("2006-01-02"), To: end.Format("2006-01-02"), Days: days,
		Items: make([]DailyUsagePoint, 0, days),
	}
	var rows []struct {
		Day        string
		TokenCount int64
	}
	// Never load editor IDs, users, revenue or raw request logs.
	if err := DB.WithContext(ctx).Model(&PublicDailyUsage{}).Select("day, request_count AS token_count").
		Where("day >= ? AND day <= ?", response.From, response.To).Order("day ASC").Scan(&rows).Error; err != nil {
		return response, err
	}
	byDate := make(map[string]int64, len(rows))
	for _, row := range rows {
		byDate[row.Day] = row.TokenCount
	}
	for i := range days {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		point := DailyUsagePoint{Date: date}
		if count, exists := byDate[date]; exists {
			point.TokenCount = &count
			response.Total += count
			response.RecordedDays++
		}
		response.Items = append(response.Items, point)
	}
	return response, nil
}

func DeletePublicDailyUsage(ctx context.Context, day string, now time.Time) error {
	if err := ValidateDailyUsageDate(day, now); err != nil {
		return err
	}
	result := DB.WithContext(ctx).Where("day = ?", day).Delete(&PublicDailyUsage{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
