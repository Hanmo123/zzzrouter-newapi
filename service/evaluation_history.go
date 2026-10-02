package service

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// A stopped task must not lose completed probes from its concurrent cohort.
// History is independent of task leases; snapshots are not. The detached write
// is bounded and never initiates another upstream request or changes routing.
func recordDegradationObservation(ctx context.Context, channel *model.Channel, name string, target DegradationTarget, observation DegradationResult, taskID, runnerID string, historyOnly bool) (*model.ChannelDegradationSample, error) {
	sample := &model.ChannelDegradationSample{
		ID: observation.ID, TaskID: taskID, Model: name, UpstreamModel: target.Model,
		RuleVersion: observation.RuleVersion, State: observation.State, MatchedMarker: observation.MatchedMarker,
		Protocol: target.Format, OutputTokenLimit: degradationOutputTokens, TimeoutMs: target.Timeout.Milliseconds(),
		ErrorCode: observation.ErrorCode, HTTPStatus: observation.HTTPStatus,
		StartedAt: observation.StartedAt, CompletedAt: observation.CompletedAt, DurationMs: observation.DurationMs, TTFTMs: observation.TTFTMs,
	}
	err := ctx.Err()
	if err == nil {
		err = model.RecordChannelDegradation(ctx, channel, sample, runnerID, historyOnly)
	}
	if err != nil && (errors.Is(err, model.ErrSystemTaskLockLost) || ctx.Err() != nil) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if writeErr := model.AppendChannelDegradationHistory(writeCtx, channel, sample); writeErr != nil {
			return sample, writeErr
		}
	}
	return sample, err
}
