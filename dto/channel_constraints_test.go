package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRoutingSyntax(t *testing.T) {
	for _, name := range []string{"gpt:floor:multiplier>=0.1:multiplier<2", "ft:gpt:org:id:multiplier=1:floor", "gpt:multiplier<=1", "gpt:multiplier>0.5"} {
		base, rule, err := ParseModelRouting(name)
		require.NoError(t, err, name)
		require.NotNil(t, rule)
		assert.NotEmpty(t, base)
	}
	for _, name := range []string{"gpt", "ft:gpt:org:id", "vendor/model:free"} {
		base, rule, err := ParseModelRouting(name)
		require.NoError(t, err)
		assert.Equal(t, name, base)
		assert.Nil(t, rule)
	}
	for _, name := range []string{":floor", "gpt:floor:floor", "gpt:floor:fastest", "gpt:multiplier=NaN", "gpt:multiplier=Inf", "gpt:multiplier=-1", "gpt:multiplier=0", "gpt:multiplier==1", "gpt:multiplier=", "gpt:multiplier= 1"} {
		_, _, err := ParseModelRouting(name)
		assert.Error(t, err, name)
	}
	for _, test := range []struct {
		op   string
		want bool
	}{{">=", true}, {"<=", true}, {"=", true}, {">", false}, {"<", false}} {
		assert.Equal(t, test.want, (MultiplierFilter{Operator: test.op, Value: 1}).Matches(1), test.op)
	}
}

func TestResolvedPinPriorityAndMerge(t *testing.T) {
	t.Run("token pin beats origin pin on a different channel", func(t *testing.T) {
		constraints := &ChannelConstraints{}
		constraints.AddPin(ChannelPin{ChannelId: 10, Source: PinSourceOriginTask, Rank: PinRankOriginTask, RetryMode: PinRetrySameChannel})
		constraints.AddPin(ChannelPin{ChannelId: 1, Source: PinSourceToken, Rank: PinRankToken, RetryMode: PinRetrySingleAttempt})

		pin, found, overridden := constraints.ResolvedPin()
		require.True(t, found)
		assert.Equal(t, 1, pin.ChannelId)
		assert.Equal(t, PinSourceToken, pin.Source)
		assert.Equal(t, PinRetrySingleAttempt, pin.RetryMode)
		require.Len(t, overridden, 1)
		assert.Equal(t, PinSourceOriginTask, overridden[0].Source)
		assert.Equal(t, 10, overridden[0].ChannelId)
	})

	t.Run("same channel pins merge to the stricter retry mode", func(t *testing.T) {
		constraints := &ChannelConstraints{}
		constraints.AddPin(ChannelPin{ChannelId: 7, Source: PinSourceOriginTask, Rank: PinRankOriginTask, RetryMode: PinRetrySameChannel})
		constraints.AddPin(ChannelPin{ChannelId: 7, Source: PinSourceToken, Rank: PinRankToken, RetryMode: PinRetrySingleAttempt})

		pin, found, overridden := constraints.ResolvedPin()
		require.True(t, found)
		assert.Equal(t, 7, pin.ChannelId)
		assert.Equal(t, PinSourceToken, pin.Source)
		assert.Equal(t, PinRetrySingleAttempt, pin.RetryMode)
		assert.Empty(t, overridden)
		assert.True(t, constraints.SuppressesRetry())
	})

	t.Run("empty set has no pin", func(t *testing.T) {
		pin, found, overridden := (*ChannelConstraints)(nil).ResolvedPin()
		assert.False(t, found)
		assert.Zero(t, pin.ChannelId)
		assert.Nil(t, overridden)
	})
}
