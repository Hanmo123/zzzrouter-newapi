package billingexpr

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBaseTokenPrices(t *testing.T) {
	expr := `len <= 100 ? tier("base",p*5+c*20) : tier("long",p*10+c*40+cr*2)`
	prices, ok := BaseTokenPrices(expr)
	require.True(t, ok)
	assert.Equal(t, map[string]float64{"p": 5, "c": 20, "cr": 0}, prices)
	// A quote must not truncate the expression used for real settlement.
	cost, trace, err := RunExprWithRequest(expr, TokenParams{P: 200, C: 100, CR: 10, Len: 210}, RequestInput{})
	require.NoError(t, err)
	assert.Equal(t, float64(6020), cost)
	assert.Equal(t, "long", trace.MatchedTier)
	prices["p"] = 999
	again, ok := BaseTokenPrices(expr)
	require.True(t, ok)
	assert.Equal(t, 5.0, again["p"])
	for _, source := range []string{`tier("x",fixed(1))`, `tier("x",p*c)`, `tier("x",p+2)`, `param("speed")=="fast" ? tier("x",p*2) : tier("x",p)`, `tier("x",u("seconds")*3)`, `tier("x",p/0)`} {
		_, ok := BaseTokenPrices(source)
		assert.False(t, ok, source)
	}
}
