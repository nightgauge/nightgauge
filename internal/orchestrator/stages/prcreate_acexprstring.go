// The printer below is a port of the Go project's go/types/exprstring.go,
// distributed under the following license:
//
// Copyright 2009 The Go Authors.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   - Redistributions of source code must retain the above copyright
//     notice, this list of conditions and the following disclaimer.
//   - Redistributions in binary form must reproduce the above copyright
//     notice, this list of conditions and the following disclaimer in the
//     documentation and/or other materials provided with the distribution.
//   - Neither the name of Google LLC nor the names of its contributors may
//     be used to endorse or promote products derived from this software
//     without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package stages

import (
	"fmt"
	"go/ast"
	"strings"
)

// acExprString returns the string types.ExprString would return for x,
// without linking go/types into the release CLI (#3675). It is a faithful
// port of go/types/exprstring.go; prcreate_acexprstring_test.go checks it
// against types.ExprString for every ast.Expr kind.
func acExprString(x ast.Expr) string {
	var b strings.Builder
	acWriteExpr(&b, x)
	return b.String()
}

func acWriteExpr(b *strings.Builder, x ast.Expr) {
	switch x := x.(type) {
	default:
		fmt.Fprintf(b, "(ast: %T)", x) // nil, ast.BadExpr, ast.KeyValueExpr

	case *ast.Ident:
		b.WriteString(x.Name)

	case *ast.Ellipsis:
		b.WriteString("...")
		if x.Elt != nil {
			acWriteExpr(b, x.Elt)
		}

	case *ast.BasicLit:
		b.WriteString(x.Value)

	case *ast.FuncLit:
		b.WriteByte('(')
		acWriteExpr(b, x.Type)
		b.WriteString(" literal)")

	case *ast.CompositeLit:
		acWriteExpr(b, x.Type)
		b.WriteByte('{')
		if len(x.Elts) > 0 {
			b.WriteString("…")
		}
		b.WriteByte('}')

	case *ast.ParenExpr:
		b.WriteByte('(')
		acWriteExpr(b, x.X)
		b.WriteByte(')')

	case *ast.SelectorExpr:
		acWriteExpr(b, x.X)
		b.WriteByte('.')
		b.WriteString(x.Sel.Name)

	case *ast.IndexExpr:
		acWriteExpr(b, x.X)
		b.WriteByte('[')
		acWriteExpr(b, x.Index)
		b.WriteByte(']')

	case *ast.IndexListExpr:
		acWriteExpr(b, x.X)
		b.WriteByte('[')
		acWriteExprList(b, x.Indices)
		b.WriteByte(']')

	case *ast.SliceExpr:
		acWriteExpr(b, x.X)
		b.WriteByte('[')
		if x.Low != nil {
			acWriteExpr(b, x.Low)
		}
		b.WriteByte(':')
		if x.High != nil {
			acWriteExpr(b, x.High)
		}
		if x.Slice3 {
			b.WriteByte(':')
			if x.Max != nil {
				acWriteExpr(b, x.Max)
			}
		}
		b.WriteByte(']')

	case *ast.TypeAssertExpr:
		acWriteExpr(b, x.X)
		b.WriteString(".(")
		acWriteExpr(b, x.Type)
		b.WriteByte(')')

	case *ast.CallExpr:
		acWriteExpr(b, x.Fun)
		b.WriteByte('(')
		acWriteExprList(b, x.Args)
		if x.Ellipsis.IsValid() {
			b.WriteString("...")
		}
		b.WriteByte(')')

	case *ast.StarExpr:
		b.WriteByte('*')
		acWriteExpr(b, x.X)

	case *ast.UnaryExpr:
		b.WriteString(x.Op.String())
		acWriteExpr(b, x.X)

	case *ast.BinaryExpr:
		acWriteExpr(b, x.X)
		b.WriteByte(' ')
		b.WriteString(x.Op.String())
		b.WriteByte(' ')
		acWriteExpr(b, x.Y)

	case *ast.ArrayType:
		b.WriteByte('[')
		if x.Len != nil {
			acWriteExpr(b, x.Len)
		}
		b.WriteByte(']')
		acWriteExpr(b, x.Elt)

	case *ast.StructType:
		b.WriteString("struct{")
		acWriteFieldList(b, x.Fields.List, "; ", false)
		b.WriteByte('}')

	case *ast.FuncType:
		b.WriteString("func")
		acWriteSigExpr(b, x)

	case *ast.InterfaceType:
		b.WriteString("interface{")
		acWriteFieldList(b, x.Methods.List, "; ", true)
		b.WriteByte('}')

	case *ast.MapType:
		b.WriteString("map[")
		acWriteExpr(b, x.Key)
		b.WriteByte(']')
		acWriteExpr(b, x.Value)

	case *ast.ChanType:
		switch x.Dir {
		case ast.SEND:
			b.WriteString("chan<- ")
		case ast.RECV:
			b.WriteString("<-chan ")
		default:
			b.WriteString("chan ")
		}
		acWriteExpr(b, x.Value)
	}
}

func acWriteSigExpr(b *strings.Builder, sig *ast.FuncType) {
	b.WriteByte('(')
	acWriteFieldList(b, sig.Params.List, ", ", false)
	b.WriteByte(')')

	res := sig.Results
	n := res.NumFields()
	if n == 0 {
		return
	}
	b.WriteByte(' ')
	if n == 1 && len(res.List[0].Names) == 0 {
		acWriteExpr(b, res.List[0].Type)
		return
	}
	b.WriteByte('(')
	acWriteFieldList(b, res.List, ", ", false)
	b.WriteByte(')')
}

func acWriteFieldList(b *strings.Builder, list []*ast.Field, sep string, iface bool) {
	for i, f := range list {
		if i > 0 {
			b.WriteString(sep)
		}
		for j, name := range f.Names {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(name.Name)
		}
		// Interface methods are written as signatures only.
		if sig, _ := f.Type.(*ast.FuncType); sig != nil && iface {
			acWriteSigExpr(b, sig)
			continue
		}
		if len(f.Names) > 0 {
			b.WriteByte(' ')
		}
		acWriteExpr(b, f.Type)
	}
}

func acWriteExprList(b *strings.Builder, list []ast.Expr) {
	for i, x := range list {
		if i > 0 {
			b.WriteString(", ")
		}
		acWriteExpr(b, x)
	}
}
