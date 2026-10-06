package billingexpr

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// ScalePriceExpression changes the amount, never context thresholds or request
// conditions. Fixed leaves need literal amounts to preserve the engine grammar.
func ScalePriceExpression(source string, factor float64) (string, error) {
	if factor < 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		return "", fmt.Errorf("invalid price factor")
	}
	if _, err := CompileFromCache(source); err != nil {
		return "", err
	}
	if factor == 1 {
		return source, nil
	}
	version, body := ParseExprVersion(source)
	if !UsesFixedPricing(source) {
		return fmt.Sprintf("v%d:(%s) * %s", version, body, strconv.FormatFloat(factor, 'g', -1, 64)), nil
	}
	tree, err := parser.Parse(body)
	if err != nil {
		return "", err
	}
	patcher := priceScalePatcher{factor: factor}
	ast.Walk(&tree.Node, &patcher)
	result := fmt.Sprintf("v%d:%s", version, tree.Node.String())
	_, err = CompileFromCache(result)
	return result, err
}

type priceScalePatcher struct{ factor float64 }

func (p *priceScalePatcher) Visit(node *ast.Node) {
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	name, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || name.Value != "tier" || len(call.Arguments) != 2 {
		return
	}
	if fixed, ok := call.Arguments[1].(*ast.CallNode); ok {
		if fn, ok := fixed.Callee.(*ast.IdentifierNode); ok && fn.Value == "fixed" && len(fixed.Arguments) == 1 {
			value, _ := requestRuleNumber(fixed.Arguments[0])
			fixed.Arguments[0] = &ast.FloatNode{Value: value * p.factor}
			return
		}
	}
	call.Arguments[1] = &ast.BinaryNode{Operator: "*", Left: call.Arguments[1], Right: &ast.FloatNode{Value: p.factor}}
}

// ProportionalTokenPrice compares every leaf and condition, not just the quoted
// base tier. Unsupported shapes remain expressions; samples cannot prove equality.
func ProportionalTokenPrice(source, reference string) (float64, bool) {
	_, a := ParseExprVersion(source)
	_, b := ParseExprVersion(reference)
	left, err := parser.Parse(a)
	if err != nil {
		return 0, false
	}
	right, err := parser.Parse(b)
	if err != nil {
		return 0, false
	}
	// Identical variable normalization is required for a coefficient comparison.
	vars := func(node ast.Node) map[string]bool {
		result := map[string]bool{}
		ast.Find(node, func(n ast.Node) bool {
			if id, ok := n.(*ast.IdentifierNode); ok {
				switch id.Value {
				case "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao":
					result[id.Value] = true
				}
			}
			return false
		})
		return result
	}
	av, bv := vars(left.Node), vars(right.Node)
	if len(av) != len(bv) {
		return 0, false
	}
	for name := range av {
		if !bv[name] {
			return 0, false
		}
	}
	ratio := -1.0
	if !comparePriceTree(left.Node, right.Node, 1, 1, &ratio) {
		return 0, false
	}
	if ratio < 0 {
		ratio = 1
	} // both contracts are identically free
	return ratio, true
}

func priceTreeFactor(node ast.Node, factor float64) (ast.Node, float64) {
	for {
		binary, ok := node.(*ast.BinaryNode)
		if !ok {
			return node, factor
		}
		number, numeric := requestRuleNumber(binary.Right)
		if numeric && (binary.Operator == "*" || (binary.Operator == "/" && number > 0)) {
			if binary.Operator == "/" {
				number = 1 / number
			}
			factor *= number
			node = binary.Left
			continue
		}
		number, numeric = requestRuleNumber(binary.Left)
		if numeric && binary.Operator == "*" {
			factor *= number
			node = binary.Right
			continue
		}
		return node, factor
	}
}
func comparePriceTree(a, b ast.Node, af, bf float64, ratio *float64) bool {
	a, af = priceTreeFactor(a, af)
	b, bf = priceTreeFactor(b, bf)
	ac, aCond := a.(*ast.ConditionalNode)
	bc, bCond := b.(*ast.ConditionalNode)
	if aCond || bCond {
		return aCond && bCond && ac.Cond.String() == bc.Cond.String() && comparePriceTree(ac.Exp1, bc.Exp1, af, bf, ratio) && comparePriceTree(ac.Exp2, bc.Exp2, af, bf, ratio)
	}
	// linearTokenPrices accepts conditionals for quotations. Here reject any
	// nested conditional so no later branch can escape the full-tree comparison.
	for _, node := range []ast.Node{a, b} {
		if ast.Find(node, func(n ast.Node) bool { _, ok := n.(*ast.ConditionalNode); return ok }) != nil {
			return false
		}
	}
	ap, err := linearTokenPrices(a)
	if err != nil {
		return false
	}
	bp, err := linearTokenPrices(b)
	if err != nil {
		return false
	}
	keys := map[string]bool{}
	for k := range ap {
		keys[k] = true
	}
	for k := range bp {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		x, y := ap[k]*af, bp[k]*bf
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || x < 0 || y < 0 {
			return false
		}
		if y == 0 {
			if x != 0 {
				return false
			}
			continue
		}
		r := x / y
		if math.IsNaN(r) || math.IsInf(r, 0) {
			return false
		}
		if *ratio < 0 {
			*ratio = r
		} else if math.Abs(r-*ratio) > 1e-12*math.Max(math.Abs(r), math.Abs(*ratio)) {
			return false
		}
	}
	return true
}
