package dto

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RoutingPriority is deliberately separate from predicates: future schedulers
// can choose another ordering without changing parsing or eligibility checks.
type RoutingPriority string

const RoutingPriorityFloor RoutingPriority = "floor"

type MultiplierFilter struct {
	Operator string
	Value    float64
}

func (f MultiplierFilter) Matches(value float64) bool {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	switch f.Operator {
	case ">=":
		return value >= f.Value
	case "<=":
		return value <= f.Value
	case ">":
		return value > f.Value
	case "<":
		return value < f.Value
	case "=":
		return value == f.Value
	}
	return false
}

type ModelRouting struct {
	Priority RoutingPriority
	Filters  []MultiplierFilter
}

// ParseModelRouting consumes only our reserved suffixes. Names containing other
// colons (fine-tunes, provider aliases, etc.) retain their existing meaning.
// Rules compose with ':' and all multiplier predicates must match.
func ParseModelRouting(name string) (string, *ModelRouting, error) {
	parts := strings.Split(name, ":")
	start := -1
	for i := 1; i < len(parts); i++ {
		if parts[i] == string(RoutingPriorityFloor) || strings.HasPrefix(parts[i], "multiplier") {
			start = i
			break
		}
	}
	if start < 0 {
		return name, nil, nil
	}
	base := strings.Join(parts[:start], ":")
	if strings.TrimSpace(base) == "" || len(parts)-start > 8 {
		return "", nil, fmt.Errorf("invalid model routing rules")
	}
	route := &ModelRouting{}
	for _, rule := range parts[start:] {
		if rule == string(RoutingPriorityFloor) {
			if route.Priority != "" {
				return "", nil, fmt.Errorf("only one routing priority is allowed")
			}
			route.Priority = RoutingPriorityFloor
			continue
		}
		text, ok := strings.CutPrefix(rule, "multiplier")
		if !ok {
			return "", nil, fmt.Errorf("unknown model routing rule")
		}
		operator := ""
		for _, op := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(text, op) {
				operator = op
				text = strings.TrimPrefix(text, op)
				break
			}
		}
		value, err := strconv.ParseFloat(text, 64)
		if operator == "" || err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) || strings.TrimSpace(text) != text {
			return "", nil, fmt.Errorf("multiplier requires >=, <=, >, < or = and a finite positive number")
		}
		route.Filters = append(route.Filters, MultiplierFilter{Operator: operator, Value: value})
	}
	return base, route, nil
}
