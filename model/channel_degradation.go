package model

import (
	"context"
	"errors"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InstanceID is internal, stable across edits, and never reused by channel copies.
func (channel *Channel) BeforeCreate(_ *gorm.DB) error {
	if channel.InstanceID == nil {
		id := uuid.NewString()
		channel.InstanceID = &id
	}
	return nil
}

// InitializeChannelInstances backfills the nullable unique column in bounded
// batches. NULL permits upgrading existing rows before assigning unique IDs.
func InitializeChannelInstances(db *gorm.DB) error {
	for {
		var ids []int
		if err := db.Model(&Channel{}).Where("instance_id IS NULL").Order("id ASC").Limit(100).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := db.Model(&Channel{}).Where("id = ? AND instance_id IS NULL", id).Update("instance_id", uuid.NewString()).Error; err != nil {
				return err
			}
		}
	}
}

// ChannelDegradationSample is append-only observation history, deliberately
// independent of disposable system tasks, consumption logs and channel deletion.
// It contains no API key, endpoint URL, raw response, prompt overrides or costs.
type ChannelDegradationSample struct {
	ID                string `json:"id" gorm:"size:36;primaryKey"`
	ChannelInstanceID string `json:"channel_instance_id" gorm:"size:36;index;index:idx_evaluation_instance_time,priority:1"`
	ChannelID         int    `json:"channel_id" gorm:"index"`
	ProviderID        int    `json:"provider_id" gorm:"index:idx_evaluation_provider_time,priority:1"`
	ChannelType       int    `json:"channel_type"`
	Model             string `json:"model" gorm:"size:255"`
	UpstreamModel     string `json:"upstream_model" gorm:"size:255"`
	TaskID            string `json:"task_id" gorm:"size:64;index"`
	RuleVersion       string `json:"rule_version" gorm:"size:32"`
	Protocol          string `json:"protocol" gorm:"size:16"`
	OutputTokenLimit  int    `json:"output_token_limit"`
	TimeoutMs         int64  `json:"timeout_ms" gorm:"type:bigint"`
	State             string `json:"state" gorm:"size:16"`
	MatchedMarker     string `json:"matched_marker" gorm:"size:64"`
	ErrorCode         string `json:"error_code" gorm:"size:64"`
	HTTPStatus        int    `json:"http_status"`
	StartedAt         int64  `json:"started_at" gorm:"type:bigint;index;index:idx_evaluation_instance_time,priority:2;index:idx_evaluation_provider_time,priority:2"` // Unix milliseconds
	CompletedAt       int64  `json:"completed_at" gorm:"type:bigint"`
	DurationMs        int64  `json:"duration_ms" gorm:"type:bigint"` // total duration, NOT TTFT
	TTFTMs            *int64 `json:"ttft_ms" gorm:"type:bigint"`
	SnapshotApplied   bool   `json:"snapshot_applied"`
}

// SetChannelDegradationExcluded changes only the quality-check policy, never
// channel status, routing, credentials or historical samples.
func SetChannelDegradationExcluded(id int, skip bool) error {
	if id <= 0 {
		return errors.New("invalid channel ID")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).Select("id").First(&channel, id).Error; err != nil {
			return err
		}
		return tx.Model(&Channel{}).Where("id = ?", id).Update("skip_degradation_check", skip).Error
	})
}

// channelDegradationInputs compares only configuration and relevant observation
// fields, not billing counters, polling positions or old channel-test timings.
// Secrets participate in an in-memory equality check, never SQL predicates/logs.
func channelDegradationInputs(channel *Channel) []any {
	return []any{channel.InstanceID, channel.Type, channel.ProviderID, channel.Key, channel.BaseURL,
		channel.Models, channel.ModelMapping, channel.TestModel, channel.OpenAIOrganization,
		channel.Setting, channel.OtherSettings, channel.ParamOverride, channel.HeaderOverride,
		channel.ChannelInfo.IsMultiKey, channel.ChannelInfo.MultiKeyStatusList, channel.SkipDegradationCheck,
		channel.IsOnline, channel.IsDegraded, channel.LastCheckedAt, channel.TTFTMs}
}

func prepareChannelDegradationSample(source *Channel, sample *ChannelDegradationSample) error {
	if source.Id <= 0 || source.InstanceID == nil || *source.InstanceID == "" {
		return errors.New("channel instance is unavailable")
	}
	if sample.State != "normal" && sample.State != "abnormal" && sample.State != "error" {
		return errors.New("invalid degradation state")
	}
	if sample.TTFTMs != nil && (*sample.TTFTMs < 0 || *sample.TTFTMs > sample.DurationMs) {
		return errors.New("invalid degradation TTFT")
	}
	if sample.ID == "" || sample.StartedAt <= 0 || sample.CompletedAt < sample.StartedAt {
		return errors.New("invalid degradation observation")
	}
	sample.ChannelInstanceID, sample.ChannelID, sample.ProviderID, sample.ChannelType = *source.InstanceID, source.Id, source.ProviderID, source.Type
	sample.SnapshotApplied = false
	return nil
}

// AppendChannelDegradationHistory retains actual finished attempts after a task
// is cancelled or loses its lease. It cannot update snapshots or task progress.
func AppendChannelDegradationHistory(ctx context.Context, source *Channel, sample *ChannelDegradationSample) error {
	if err := prepareChannelDegradationSample(source, sample); err != nil {
		return err
	}
	return NewRelationalEvaluationHistoryStore(DB).Append(ctx, sample)
}

// RecordChannelDegradation keeps the result even when the channel was deleted,
// replaced or edited during the network request. Only an unchanged instance
// receives the latest snapshot. Successful streams also publish measured TTFT;
// errors never replace the last successful latency. Routing state is unchanged.
func RecordChannelDegradation(ctx context.Context, source *Channel, sample *ChannelDegradationSample, runnerID string, historyOnly ...bool) error {
	if err := prepareChannelDegradationSample(source, sample); err != nil {
		return err
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if sample.TaskID != "" {
			// Check the lease in the write transaction as well as honoring ctx:
			// heartbeat cancellation alone leaves a window after lock loss.
			var lease SystemTaskLock
			err := lockForUpdate(tx).Where("type = ? AND task_id = ? AND locked_by = ? AND locked_until >= ?", SystemTaskTypeChannelDegradation, sample.TaskID, runnerID, common.GetTimestamp()).First(&lease).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSystemTaskLockLost
			}
			if err != nil {
				return err
			}
		}
		var current Channel
		err := lockForUpdate(tx).First(&current, source.Id).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		unchanged := err == nil && current.Status == common.ChannelStatusEnabled && !current.SkipDegradationCheck && reflect.DeepEqual(channelDegradationInputs(source), channelDegradationInputs(&current))
		switch sample.ErrorCode {
		case "cancelled", "invalid_target", "invalid_format", "invalid_proxy", "invalid_request":
			// Retain the attempt, but client/configuration failures cannot publish
			// an offline snapshot as if an upstream measurement had failed.
			unchanged = false
		}
		if unchanged && !(len(historyOnly) > 0 && historyOnly[0]) && (current.LastCheckedAt == nil || *current.LastCheckedAt <= sample.StartedAt/1000) {
			var degraded *bool
			online := sample.State != "error"
			if online {
				value := sample.State == "abnormal"
				degraded = &value
			}
			updates := map[string]any{"is_degraded": degraded, "is_online": online, "last_checked_at": sample.CompletedAt / 1000}
			if online && sample.TTFTMs != nil {
				updates["ttft_ms"] = *sample.TTFTMs
			}
			if err := tx.Model(&Channel{}).Where("id = ?", source.Id).Updates(updates).Error; err != nil {
				return err
			}
			sample.SnapshotApplied = true
		}
		// Insert and snapshot update commit together; a duplicate sample UUID
		// rolls the transaction back instead of rewriting historical results.
		return NewRelationalEvaluationHistoryStore(tx).Append(ctx, sample)
	})
}
