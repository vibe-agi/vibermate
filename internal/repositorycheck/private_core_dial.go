package repositorycheck

import (
	"go/ast"
	"go/token"
	"strconv"
)

// privateCoreDialSymbols recognizes only the reviewed private-core dial shapes.
// Receiver binding is syntactic; private field provenance still needs review.
func privateCoreDialSymbols(relative string, parsed *ast.File, imports map[string]string) map[*ast.SelectorExpr]bool {
	allowed := make(map[*ast.SelectorExpr]bool)
	if parsed.Name.Name != "vlesscore" {
		return allowed
	}
	var method, network, field string
	switch relative {
	case "internal/vlesscore/socks.go":
		method, network, field = "authenticate", "tcp4", "endpoint"
	case "internal/vlesscore/datagram.go":
		method, network, field = "DialUDP", "unix", "udpPath"
	default:
		return allowed
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != method || function.Recv == nil || len(function.Recv.List) != 1 || function.Body == nil {
			continue
		}
		receiver := function.Recv.List[0]
		pointer, ok := receiver.Type.(*ast.StarExpr)
		if !ok || len(receiver.Names) != 1 || receiver.Names[0].Obj == nil {
			continue
		}
		client, ok := pointer.X.(*ast.Ident)
		if !ok || client.Name != "Client" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if _, nested := node.(*ast.FuncLit); nested {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 || call.Ellipsis != token.NoPos {
				return true
			}
			dial, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || dial.Sel.Name != "DialContext" {
				return true
			}
			parenthesized, ok := dial.X.(*ast.ParenExpr)
			if !ok {
				return true
			}
			addressOf, ok := parenthesized.X.(*ast.UnaryExpr)
			if !ok || addressOf.Op != token.AND {
				return true
			}
			literal, ok := addressOf.X.(*ast.CompositeLit)
			if !ok || len(literal.Elts) != 0 {
				return true
			}
			dialer, ok := literal.Type.(*ast.SelectorExpr)
			if !ok || dialer.Sel.Name != "Dialer" {
				return true
			}
			importName, ok := dialer.X.(*ast.Ident)
			if !ok || importName.Obj != nil || imports[importName.Name] != "net" {
				return true
			}
			networkLiteral, ok := call.Args[1].(*ast.BasicLit)
			if !ok || networkLiteral.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(networkLiteral.Value)
			if err != nil || value != network {
				return true
			}
			destination, ok := call.Args[2].(*ast.SelectorExpr)
			if !ok || destination.Sel.Name != field {
				return true
			}
			owner, ok := destination.X.(*ast.Ident)
			if !ok || owner.Obj != receiver.Names[0].Obj {
				return true
			}
			allowed[dialer] = true
			return true
		})
	}
	return allowed
}
