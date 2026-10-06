package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTaskProcurementCostIndependentOfFreeGroup(t *testing.T) {
	truncate(t)
	seedUser(t, 907, 100000)
	seedChannel(t, 907)
	task := makeTask(907, 907, 0, 0, "wallet", 0)
	charge := &types.RelayCharge{BaseGroupRatio: 0, ChannelPublicRatio: .2, ServiceFeeEnabled: true, ServiceFeeRate: .05, UpstreamDiscount: common.GetPointer(.8)}
	charge.CaptureBaseQuota(1000)
	task.PrivateData.BillingContext.RelayCharge = charge
	require.NoError(t, model.DB.Create(task).Error)
	RecalculateTaskQuotaWithBase(context.Background(), task, 0, 2000, "actual usage")
	var loaded model.Task
	require.NoError(t, model.DB.First(&loaded, task.ID).Error)
	assert.Zero(t, loaded.Quota)
	cost, clamp := loaded.PrivateData.BillingContext.RelayCharge.CostQuota()
	assert.Nil(t, clamp)
	assert.Equal(t, 320, cost)
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ?", 907).Find(&logs).Error)
	require.Len(t, logs, 1)
	var other struct {
		Admin struct {
			Cost  int `json:"upstream_cost_quota"`
			Total int `json:"upstream_total_cost_quota"`
		} `json:"admin_info"`
	}
	require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
	assert.Equal(t, 160, other.Admin.Cost)
	assert.Equal(t, 320, other.Admin.Total)
	assert.True(t, RefundTaskQuota(context.Background(), &loaded, "failed"))
	require.NoError(t, model.DB.First(&loaded, task.ID).Error)
	cost, _ = loaded.PrivateData.BillingContext.RelayCharge.CostQuota()
	assert.Zero(t, cost)
	var user model.User
	require.NoError(t, model.DB.First(&user, 907).Error)
	assert.Equal(t, 100000, user.Quota)
}
