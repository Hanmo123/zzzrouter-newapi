package billingexpr

import "github.com/expr-lang/expr/ast"

// Only known service_tier predicates are resolved for quotation. Other request
// probes, time rules, and arbitrary functions retain the fail-closed behavior.
type serviceTierQuotePatcher struct{ serviceTier string }

func (p *serviceTierQuotePatcher) Visit(node *ast.Node) {
	conditional, ok := (*node).(*ast.ConditionalNode)
	if !ok {
		return
	}
	matched, supported := serviceTierCondition(conditional.Cond, p.serviceTier)
	if !supported {
		return
	}
	if matched {
		ast.Patch(node, conditional.Exp1)
	} else {
		ast.Patch(node, conditional.Exp2)
	}
}

func serviceTierCondition(node ast.Node, serviceTier string) (bool, bool) {
	binary, ok := node.(*ast.BinaryNode)
	if !ok {
		return false, false
	}
	if binary.Operator == "||" || binary.Operator == "&&" {
		left, leftOK := serviceTierCondition(binary.Left, serviceTier)
		right, rightOK := serviceTierCondition(binary.Right, serviceTier)
		if binary.Operator == "||" {
			return left || right, leftOK && rightOK
		}
		return left && right, leftOK && rightOK
	}
	if binary.Operator != "==" {
		return false, false
	}
	call, ok := binary.Left.(*ast.CallNode)
	value, stringOK := binary.Right.(*ast.StringNode)
	if !ok || !stringOK || value.Value == "" || len(call.Arguments) != 1 {
		return false, false
	}
	name, nameOK := call.Callee.(*ast.IdentifierNode)
	argument, argumentOK := call.Arguments[0].(*ast.StringNode)
	if !nameOK || name.Value != "param" || !argumentOK || argument.Value != "service_tier" {
		return false, false
	}
	return serviceTier == value.Value, true
}
