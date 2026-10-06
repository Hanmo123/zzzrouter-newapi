package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

// The relay database matrix additionally exercises model configuration across
// SQLite/MySQL/PostgreSQL, including repeated schema migrations and routing.
func TestChannelModelMetadata(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Provider{}))
	p := Provider{Name: "metadata-models"}
	require.NoError(t, p.Save())
	cfg := ChannelModelConfig{PublicRatio: common.GetPointer(.125), UpstreamDiscount: common.GetPointer(.8), IsOnline: common.GetPointer(false), IsDegraded: common.GetPointer(true), TTFTMs: common.GetPointer(int64(0)), LastCheckedAt: common.GetPointer(int64(1700000123))}
	ch := Channel{Name: "metadata", Key: "key-a\nkey-b", Models: "a,b", Group: "default", ProviderID: p.ID, Status: common.ChannelStatusManuallyDisabled, ModelConfigs: ChannelModelConfigs{"a": cfg, "b": {PublicRatio: common.GetPointer(0.0)}}}
	require.NoError(t, ch.Insert())
	rename := Channel{Id: ch.Id, Name: "renamed"}
	require.NoError(t, rename.Update())
	assert.Equal(t, cfg, rename.ModelConfig("a"))
	assert.Zero(t, rename.ModelConfig("b").ChargeRatio())
	assert.Equal(t, ch.Key, rename.Key)
	// Connectivity/status bookkeeping must not overwrite model observations.
	rename.UpdateResponseTime(900)
	status := Channel{Id: ch.Id, Status: common.ChannelStatusEnabled}
	require.NoError(t, status.saveStatusState())
	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, cfg, loaded.ModelConfig("a"))
	revision := loaded.ModelConfigs.Revision()
	// Explicit null clears observations; a later stale full-map save is rejected.
	cleared := cfg
	cleared.IsOnline = nil
	cleared.IsDegraded = nil
	cleared.TTFTMs = nil
	cleared.LastCheckedAt = nil
	patch := Channel{Id: ch.Id, ModelConfigsRevision: &revision, ModelConfigs: ChannelModelConfigs{"a": cleared, "b": {PublicRatio: common.GetPointer(0.0)}}}
	require.NoError(t, patch.Update())
	assert.Equal(t, cleared, patch.ModelConfig("a"))
	stale := Channel{Id: ch.Id, ModelConfigsRevision: &revision, ModelConfigs: ch.ModelConfigs}
	require.ErrorContains(t, stale.Update(), "reload")
	loaded, err = GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Nil(t, loaded.ModelConfig("a").IsOnline)
	for _, bad := range []ChannelModelConfig{{PublicRatio: common.GetPointer(-1.0)}, {PublicRatio: common.GetPointer(math.NaN())}, {PublicRatio: common.GetPointer(math.Inf(1))}, {UpstreamDiscount: common.GetPointer(1.1)}, {TTFTMs: common.GetPointer(int64(-1))}, {LastCheckedAt: common.GetPointer(int64(253402300800))}} {
		patch := Channel{Id: ch.Id, ModelConfigs: ChannelModelConfigs{"a": bad}}
		assert.Error(t, patch.Update())
		assert.Error(t, patch.Save())
		patch.Id = 0
		assert.Error(t, patch.Insert())
	}
	batch := []Channel{{Name: "batch-valid", Key: "x", ProviderID: p.ID}, {Name: "batch-invalid", Key: "y", ProviderID: p.ID, ModelConfigs: ChannelModelConfigs{"a": {UpstreamDiscount: common.GetPointer(-1.0)}}}}
	require.Error(t, BatchInsertChannels(batch))
	var count int64
	require.NoError(t, DB.Model(&Channel{}).Where("name = ?", "batch-valid").Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, BatchInsertChannels(batch[:1]))
	assert.Equal(t, 1.0, batch[0].ModelConfig("a").ChargeRatio())
	clear := Channel{Id: ch.Id, ModelConfigs: ChannelModelConfigs{}}
	require.NoError(t, clear.Update())
	assert.Empty(t, clear.ModelConfigs)
	encoded, err := common.Marshal(loaded)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"model_configs"`)
	assert.NotContains(t, string(encoded), `"upstream_ratio"`)
}
