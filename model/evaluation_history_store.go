package model

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

const EvaluationDayMs int64 = 24 * 60 * 60 * 1000

// The storage contract contains only immutable evaluation facts and time-range
// queries, not SQL transactions, channel authorization or UI-specific charts.
// A future ClickHouse adapter can implement this contract without changing the
// public API. For dual writes, replicate committed UUID-keyed facts through a
// durable outbox; never perform a remote write inside the snapshot transaction.
type EvaluationHistoryStore interface {
	Append(context.Context, *ChannelDegradationSample) error
	Read(context.Context, EvaluationHistoryScope) (EvaluationHistory, error)
}

// Authorization is resolved against the primary database before querying a
// history store. Instance IDs prevent deleted/copied/reassigned channels from
// publishing another channel's history, including with a future external store.
type EvaluationHistoryScope struct {
	ProviderID int
	Instances  []string
	Model      string
	From       int64
	To         int64
}

type EvaluationBucket struct {
	StartedAt     int64    `json:"started_at"`
	Total         int64    `json:"total"`
	Online        int64    `json:"online"`
	Degraded      int64    `json:"degraded"`
	Errors        int64    `json:"errors"`
	Unknown       int64    `json:"unknown"`
	TTFTSamples   int64    `json:"ttft_samples"`
	AverageTTFTMs *float64 `json:"average_ttft_ms"`
}

type PublicEvaluationSample struct {
	ID          string `json:"id"`
	ChannelID   int    `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Model       string `json:"model"`
	State       string `json:"state"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at"`
	TTFTMs      *int64 `json:"ttft_ms"`
}

type EvaluationHistory struct {
	Buckets []EvaluationBucket       `json:"buckets"`
	Recent  []PublicEvaluationSample `json:"recent"`
}

type relationalEvaluationHistoryStore struct{ db *gorm.DB }

// Bind to the existing primary database (Postgres in this deployment), or its
// transaction when appending. SQLite/MySQL remain supported for other installs.
func NewRelationalEvaluationHistoryStore(db *gorm.DB) EvaluationHistoryStore {
	return &relationalEvaluationHistoryStore{db: db}
}

func (store *relationalEvaluationHistoryStore) Append(ctx context.Context, sample *ChannelDegradationSample) error {
	return store.db.WithContext(ctx).Create(sample).Error
}

func (store *relationalEvaluationHistoryStore) Read(ctx context.Context, scope EvaluationHistoryScope) (EvaluationHistory, error) {
	history := EvaluationHistory{Buckets: []EvaluationBucket{}, Recent: []PublicEvaluationSample{}}
	if len(scope.Instances) == 0 {
		return history, nil
	}
	// Aggregate and recent facts must share a snapshot, even while probes are
	// completing concurrently. SQLite uses its normal read transaction.
	var options *sql.TxOptions
	if store.db.Dialector.Name() != "sqlite" {
		options = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&ChannelDegradationSample{}).
			Where("provider_id = ? AND channel_instance_id IN ? AND started_at >= ? AND started_at <= ?", scope.ProviderID, scope.Instances, scope.From, scope.To)
		if scope.Model != "" {
			if store.db.Dialector.Name() == "mysql" {
				query = query.Where("BINARY model = BINARY ?", scope.Model)
			} else {
				query = query.Where("model = ?", scope.Model)
			}
		}
		// Integer division is exact on Postgres/SQLite; MySQL's / needs FLOOR.
		bucket := "(started_at / 86400000) * 86400000"
		if store.db.Dialector.Name() == "mysql" {
			bucket = "FLOOR(started_at / 86400000) * 86400000"
		}
		// Cancellation/configuration errors are retained but are not evidence of
		// upstream downtime. Keep the classification identical in both projections.
		unknown := "COALESCE(error_code, '') IN ('cancelled', 'invalid_target', 'invalid_format', 'invalid_proxy', 'invalid_request')"
		err := query.Session(&gorm.Session{}).Select(bucket + ` AS started_at,
		COUNT(*) AS total,
		SUM(CASE WHEN state IN ('normal', 'abnormal') THEN 1 ELSE 0 END) AS online,
		SUM(CASE WHEN state = 'abnormal' THEN 1 ELSE 0 END) AS degraded,
		SUM(CASE WHEN state = 'error' AND NOT (` + unknown + `) THEN 1 ELSE 0 END) AS errors,
		SUM(CASE WHEN state = 'error' AND (` + unknown + `) THEN 1 ELSE 0 END) AS unknown,
		COUNT(CASE WHEN state <> 'error' THEN ttft_ms ELSE NULL END) AS ttft_samples,
		AVG(CASE WHEN state <> 'error' THEN ttft_ms ELSE NULL END) AS average_ttft_ms`).
			Group(bucket).Order("started_at ASC").Scan(&history.Buckets).Error
		if err != nil {
			return err
		}
		// An explicit projection is essential: no endpoint, key, upstream model,
		// marker, task ID, raw error/response or configuration reaches the public UI.
		return query.Session(&gorm.Session{}).
			Select("id, channel_id, model, CASE WHEN state = 'error' AND (" + unknown + ") THEN 'unknown' ELSE state END AS state, started_at, completed_at, ttft_ms").
			Order("started_at DESC, id DESC").Limit(50).Scan(&history.Recent).Error
	}, options)
	return history, err
}
