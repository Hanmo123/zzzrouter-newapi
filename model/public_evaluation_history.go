package model

import (
	"context"
	"errors"
	"time"
)

type PublicHistoryQuery struct {
	ProviderID int
	ChannelID  int
	Days       int
	Model      string
}

func (query *PublicHistoryQuery) Normalize() error {
	if query.Days == 0 {
		query.Days = 30
	}
	if query.ProviderID <= 0 || query.ChannelID < 0 || (query.Days != 7 && query.Days != 30 && query.Days != 90) {
		return errors.New("invalid provider/channel ID or days (7, 30 or 90)")
	}
	catalog := PublicCatalogQuery{Model: query.Model}
	return catalog.Normalize()
}

type PublicProviderHistory struct {
	ProviderID    int      `json:"provider_id"`
	ProviderName  string   `json:"provider_name"`
	From          int64    `json:"from"`
	To            int64    `json:"to"`
	Days          int      `json:"days"`
	Total         int64    `json:"total"`
	Online        int64    `json:"online"`
	Degraded      int64    `json:"degraded"`
	Errors        int64    `json:"errors"`
	Unknown       int64    `json:"unknown"`
	AverageTTFTMs *float64 `json:"average_ttft_ms"`
	EvaluationHistory
}

func ListPublicProviderHistory(ctx context.Context, query PublicHistoryQuery, now time.Time, store EvaluationHistoryStore) (PublicProviderHistory, error) {
	if err := query.Normalize(); err != nil {
		return PublicProviderHistory{}, err
	}
	today := now.UnixMilli() / EvaluationDayMs * EvaluationDayMs
	response := PublicProviderHistory{
		ProviderID: query.ProviderID, From: today - int64(query.Days-1)*EvaluationDayMs,
		To: now.UnixMilli(), Days: query.Days,
		EvaluationHistory: EvaluationHistory{Buckets: []EvaluationBucket{}, Recent: []PublicEvaluationSample{}},
	}
	// Offline/degraded observations must remain visible in history; do not use
	// the directory's default latest-snapshot filters to erase past outages.
	channels := publicCatalogChannels(PublicCatalogQuery{ProviderID: query.ProviderID, Model: query.Model, IsOnline: "all", IsDegraded: "all"}).WithContext(ctx)
	if query.ChannelID > 0 {
		channels = channels.Where("c.id = ?", query.ChannelID)
	}
	var visible []struct {
		ID           int
		Name         string
		InstanceID   string
		ProviderName string
	}
	if err := channels.Select("c.id, c.name, c.instance_id, provider.name AS provider_name").Limit(5001).Scan(&visible).Error; err != nil {
		return response, err
	}
	if len(visible) > 5000 {
		return response, errors.New("too many channels; select one channel")
	}
	scope := EvaluationHistoryScope{ProviderID: query.ProviderID, Model: query.Model, From: response.From, To: response.To}
	names := make(map[int]string, len(visible))
	for _, channel := range visible {
		if channel.InstanceID != "" {
			response.ProviderName = channel.ProviderName
			scope.Instances = append(scope.Instances, channel.InstanceID)
			names[channel.ID] = channel.Name
		}
	}
	history := EvaluationHistory{Recent: []PublicEvaluationSample{}}
	if len(scope.Instances) > 0 {
		var err error
		history, err = store.Read(ctx, scope)
		if err != nil {
			return response, err
		}
	}
	byDay := make(map[int64]EvaluationBucket, len(history.Buckets))
	var measured int64
	var latencyTotal float64
	for _, bucket := range history.Buckets {
		byDay[bucket.StartedAt] = bucket
		response.Total += bucket.Total
		response.Online += bucket.Online
		response.Degraded += bucket.Degraded
		response.Errors += bucket.Errors
		response.Unknown += bucket.Unknown
		if bucket.AverageTTFTMs != nil && bucket.TTFTSamples > 0 {
			measured += bucket.TTFTSamples
			latencyTotal += *bucket.AverageTTFTMs * float64(bucket.TTFTSamples)
		}
	}
	if measured > 0 {
		mean := latencyTotal / float64(measured)
		response.AverageTTFTMs = &mean
	}
	// Zero-count days are explicit gaps, never 100% uptime or 0 ms latency.
	for day := response.From; day <= today; day += EvaluationDayMs {
		bucket, exists := byDay[day]
		if !exists {
			bucket.StartedAt = day
		}
		response.Buckets = append(response.Buckets, bucket)
	}
	response.Recent = history.Recent
	for i := range response.Recent {
		response.Recent[i].ChannelName = names[response.Recent[i].ChannelID]
	}
	return response, nil
}
