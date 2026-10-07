package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

// Some New API catalogs publish expression_pricing instead of billing_expr.
// Prices are USD per million tokens before the group multiplier. Their editor
// uses len for inclusive context boundaries and param("service_tier") for the
// fast/flex/priority multipliers. Accept only this complete, known contract;
// silently ignoring additional fields could discard a billing rule.
func newAPIStructuredPriceExpression(raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return "", errors.New("Invalid upstream structured pricing")
	}
	for key := range fields {
		if key != "tiers" && key != "service_multipliers" {
			return "", fmt.Errorf("Unsupported upstream structured pricing field: %s", key)
		}
	}
	var tiers []map[string]*float64
	if err := common.Unmarshal(fields["tiers"], &tiers); err != nil || len(tiers) == 0 || len(tiers) > 64 {
		return "", errors.New("Invalid upstream pricing tiers")
	}
	var expression strings.Builder
	nextMin := 0.0
	for i, tier := range tiers {
		for key := range tier {
			switch key {
			case "min_input", "max_input", "input", "output", "cache_read", "cache_write", "cache_write_1h":
			default:
				return "", fmt.Errorf("Unsupported upstream tier price field: %s", key)
			}
		}
		minInput, maxInput := tier["min_input"], tier["max_input"]
		if minInput == nil || *minInput != nextMin {
			return "", errors.New("Upstream pricing tiers must start at zero and be ordered without gaps or overlaps")
		}
		if i == len(tiers)-1 {
			if maxInput != nil {
				return "", errors.New("The final upstream pricing tier must have no upper bound")
			}
		} else {
			if maxInput == nil || !validImportPrice(*maxInput) || *maxInput < *minInput || math.Trunc(*maxInput) != *maxInput || *maxInput > (1<<53)-2 {
				return "", errors.New("Invalid upstream pricing tier boundary")
			}
			nextMin = *maxInput + 1
			fmt.Fprintf(&expression, "len <= %s ? ", strconv.FormatFloat(*maxInput, 'f', 0, 64))
		}
		// Resolve missing cache-write prices before emitting the expression, so
		// quotation and settlement share the explicit cc1h -> cc -> p contract.
		// Only absence inherits: explicit zero stays free; null/invalid is rejected.
		if _, exists := tier["cache_write"]; !exists {
			tier["cache_write"] = tier["input"]
		}
		if _, exists := tier["cache_write_1h"]; !exists {
			tier["cache_write_1h"] = tier["cache_write"]
		}
		prices := map[string]float64{}
		for field, variable := range map[string]string{"input": "p", "output": "c", "cache_read": "cr", "cache_write": "cc", "cache_write_1h": "cc1h"} {
			price := tier[field]
			if price == nil || !validImportPrice(*price) {
				return "", fmt.Errorf("Missing or invalid upstream tier price: %s", field)
			}
			prices[variable] = *price
		}
		expression.WriteString(strings.Replace(tokenPriceExpression(prices), `"base"`, fmt.Sprintf(`"tier_%d"`, i+1), 1))
		if i < len(tiers)-1 {
			expression.WriteString(" : ")
		}
	}
	var multipliers map[string]*float64
	if rawMultipliers, exists := fields["service_multipliers"]; exists {
		if err := common.Unmarshal(rawMultipliers, &multipliers); err != nil || multipliers == nil {
			return "", errors.New("Invalid upstream service multipliers")
		}
	}
	names := make([]string, 0, len(multipliers))
	for name, multiplier := range multipliers {
		if !slices.Contains([]string{"fast", "flex", "priority"}, name) || multiplier == nil || !validImportPrice(*multiplier) {
			return "", errors.New("Unsupported or invalid upstream service multiplier")
		}
		names = append(names, name)
	}
	slices.Sort(names)
	result := "v1:(" + expression.String() + ")"
	for _, name := range names {
		if *multipliers[name] != 1 {
			result += fmt.Sprintf(` * (param("service_tier") == %q ? %s : 1)`, name, strconv.FormatFloat(*multipliers[name], 'g', -1, 64))
		}
	}
	if len(result) > 16384 {
		return "", errors.New("Upstream billing expression is too long")
	}
	if _, err := billingexpr.CompileFromCache(result); err != nil {
		return "", err
	}
	return result, nil
}
