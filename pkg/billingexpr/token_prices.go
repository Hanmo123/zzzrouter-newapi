package billingexpr

import (
	"errors"
	"maps"
	"math"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

var tokenPriceCache = struct {
	sync.Mutex
	values map[string]map[string]float64
}{values: make(map[string]map[string]float64)}

// BaseTokenPrices reads linear token prices from the branch selected at zero
// context length. All other conditions/units are incomparable. The original AST
// preserves explicit zero coefficients that the execution optimizer may remove.
// This is a quotation only: settlement always runs the complete expression.
func BaseTokenPrices(source string) (map[string]float64, bool) {
	tokenPriceCache.Lock()
	defer tokenPriceCache.Unlock()
	if prices, found := tokenPriceCache.values[source]; found {
		return maps.Clone(prices), prices != nil
	}
	_, body := ParseExprVersion(source)
	tree, err := parser.Parse(body)
	var prices map[string]float64
	if err == nil {
		prices, err = linearTokenPrices(tree.Node)
		if err != nil {
			prices = nil
		} else {
			// Normalization sees variables used in any tier. A category priced
			// only in a later tier has an explicit zero price in the base tier.
			ast.Find(tree.Node, func(node ast.Node) bool {
				if name, ok := node.(*ast.IdentifierNode); ok {
					switch name.Value {
					case "p", "c", "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao":
						if _, exists := prices[name.Value]; !exists {
							prices[name.Value] = 0
						}
					}
				}
				return false
			})
		}
	}
	if len(tokenPriceCache.values) >= maxCacheSize {
		clear(tokenPriceCache.values)
	}
	tokenPriceCache.values[source] = prices
	return maps.Clone(prices), prices != nil
}

func linearTokenPrices(node ast.Node) (map[string]float64, error) {
	invalid := errors.New("expression has no comparable base token prices")
	switch n := node.(type) {
	case *ast.ConditionalNode:
		// Context-length tiers are comparable at their base tier. Request-dependent,
		// output-dependent, and nonlinear pricing must not enter multiplier routing.
		valid := true
		ast.Find(n.Cond, func(part ast.Node) bool {
			switch value := part.(type) {
			case *ast.IdentifierNode:
				valid = valid && value.Value == "len"
			case *ast.CallNode, *ast.MemberNode:
				valid = false
			}
			return false
		})
		if !valid {
			return nil, invalid
		}
		program, err := expr.Compile(n.Cond.String(), expr.Env(map[string]float64{"len": 0}), expr.AsBool())
		if err != nil {
			return nil, invalid
		}
		value, err := expr.Run(program, map[string]float64{"len": 0})
		if err != nil {
			return nil, invalid
		}
		if value == true {
			return linearTokenPrices(n.Exp1)
		}
		return linearTokenPrices(n.Exp2)
	case *ast.CallNode:
		name, ok := n.Callee.(*ast.IdentifierNode)
		if !ok || name.Value != "tier" || len(n.Arguments) != 2 {
			return nil, invalid
		}
		return linearTokenPrices(n.Arguments[1])
	case *ast.IdentifierNode:
		switch n.Value {
		case "p", "c", "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao":
			return map[string]float64{n.Value: 1}, nil
		}
	case *ast.IntegerNode:
		if n.Value == 0 {
			return map[string]float64{}, nil
		}
	case *ast.FloatNode:
		if n.Value == 0 {
			return map[string]float64{}, nil
		}
	case *ast.BinaryNode:
		if n.Operator == "+" {
			left, err := linearTokenPrices(n.Left)
			if err != nil {
				return nil, err
			}
			right, err := linearTokenPrices(n.Right)
			if err != nil {
				return nil, err
			}
			for key, price := range right {
				left[key] += price
				if math.IsInf(left[key], 0) || math.IsNaN(left[key]) {
					return nil, invalid
				}
			}
			return left, nil
		}
		if n.Operator == "*" || n.Operator == "/" {
			price, numeric := requestRuleNumber(n.Right)
			term := n.Left
			if !numeric && n.Operator == "*" {
				price, numeric = requestRuleNumber(n.Left)
				term = n.Right
			}
			if !numeric || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
				return nil, invalid
			}
			if n.Operator == "/" {
				if price == 0 {
					return nil, invalid
				}
				price = 1 / price
			}
			prices, err := linearTokenPrices(term)
			if err != nil {
				return nil, err
			}
			for key, value := range prices {
				prices[key] = value * price
				if math.IsInf(prices[key], 0) || math.IsNaN(prices[key]) {
					return nil, invalid
				}
			}
			return prices, nil
		}
	}
	return nil, invalid
}
