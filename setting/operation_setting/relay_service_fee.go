package operation_setting

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const RelayServiceFeeOption = "RelayServiceFee"

type RelayServiceFee struct {
	Enabled bool    `json:"enabled"`
	Rate    float64 `json:"rate"`
}

var relayServiceFee atomic.Pointer[RelayServiceFee]

func init()                               { relayServiceFee.Store(&RelayServiceFee{Enabled: true, Rate: 0.05}) }
func GetRelayServiceFee() RelayServiceFee { return *relayServiceFee.Load() }
func ParseRelayServiceFee(value string) (RelayServiceFee, error) {
	var input struct {
		Enabled *bool    `json:"enabled"`
		Rate    *float64 `json:"rate"`
	}
	if err := common.UnmarshalJsonStr(value, &input); err != nil {
		return RelayServiceFee{}, fmt.Errorf("invalid service fee settings")
	}
	if input.Enabled == nil || input.Rate == nil || math.IsNaN(*input.Rate) || math.IsInf(*input.Rate, 0) || *input.Rate < 0 || *input.Rate > 1 {
		return RelayServiceFee{}, fmt.Errorf("service fee requires enabled and a finite rate between 0 and 1")
	}
	return RelayServiceFee{Enabled: *input.Enabled, Rate: *input.Rate}, nil
}
func LoadRelayServiceFee(value string) error {
	fee, err := ParseRelayServiceFee(value)
	if err != nil {
		return err
	}
	relayServiceFee.Store(&fee)
	return nil
}
