package stages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
)

// acGoTypeDepth bounds named-type resolution, so a cyclic declaration
// (`type A B; type B A`) ends as unresolved instead of looping.
const acGoTypeDepth = 16

// acGoFieldRE parses a Go field-value span `name: value` (#3424): name is a
// Go identifier, whitespace follows the colon (gofmt), and value is one
// quoted literal or an unquoted token that contains no colon and does not
// start with `/`, a quote or a backtick, so URL and host:port spans are no
// field spans. It is narrower than acFieldSpanRE: a span the guard accepts
// but this rejects (`On: a:b`) is judged in a Go file by its text.
var acGoFieldRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):\s+("[^"]*"|` + "`[^`]*`" + `|[^\s:/"` + "`" + `][^\s:]*)$`)

// acFieldSpanRE recognises a field span for the not-verifiable guard
// (#3424): a Go identifier, a colon, at least one whitespace character, then
// a value. acIsFieldSpan also rejects any span containing `://`, so a URL or
// a host:port span keeps its text-match judgement.
var acFieldSpanRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*:\s+\S`)

// acIsFieldSpan reports that tok is a `name: value` field span.
func acIsFieldSpan(tok string) bool {
	tok = strings.Trim(tok, " ")
	return !strings.Contains(tok, "://") && acFieldSpanRE.MatchString(tok)
}

// acGoFile is one Go file parsed for field-value spans; f is nil when the
// file could not be read or parsed.
type acGoFile struct {
	fset  *token.FileSet
	f     *ast.File
	types map[string]ast.Expr
}

// acGoFieldMatch is the answer for one span in one file. added holds the
// 1-based lines the diff adds where a composite literal sets the field to
// the value. Only added lines count: a positional match elsewhere in the
// file (another table row) is not what the change delivers (#3424 AC 7).
// undecided reports that a positional literal on an added line has a struct
// type whose field order this file cannot resolve, or that the file does not
// parse: the span may be set there, so its absence is not evidence of unmet.
type acGoFieldMatch struct {
	added     []int
	undecided bool
}

// goFieldValue matches a `name: value` span against the Go file p (#3424):
// a composite literal element sets field name to value, keyed (`name:
// value`) or positional, the position resolved from the struct type's field
// order with go/ast on the branch content. Types resolved: inline struct
// types, elided element types of slice, array and map literals, and named
// types declared in the same file. ok=false when tok is no field span or p
// is not a Go file; an unreadable or unparsable Go file is undecided.
func (d *ACDiff) goFieldValue(tok, p string) (acGoFieldMatch, bool) {
	m := acGoFieldRE.FindStringSubmatch(strings.Trim(tok, " "))
	if m == nil || !strings.HasSuffix(p, ".go") {
		return acGoFieldMatch{}, false
	}
	key := p + "\x00" + tok
	if got, seen := d.gofields[key]; seen {
		return got, true
	}
	got, ok := d.matchGoField(m[1], m[2], p)
	if ok {
		if d.gofields == nil {
			d.gofields = map[string]acGoFieldMatch{}
		}
		d.gofields[key] = got
	}
	return got, ok
}

// matchGoField is goFieldValue's uncached walk for field name and value want.
func (d *ACDiff) matchGoField(name, want, p string) (acGoFieldMatch, bool) {
	g := d.goFile(p)
	if g == nil || g.f == nil {
		// An unreadable or unparsable file cannot show the field order.
		return acGoFieldMatch{undecided: true}, true
	}
	added := map[int]bool{}
	if f := d.Files[p]; f != nil {
		for _, l := range f.Added {
			added[l.N] = true
		}
	}
	var out acGoFieldMatch
	set := func(e ast.Expr) {
		if !acGoValueIs(e, want) {
			return
		}
		if n := g.fset.Position(e.Pos()).Line; added[n] {
			out.added = append(out.added, n)
		}
	}
	var walk func(lit *ast.CompositeLit, typ ast.Expr)
	walk = func(lit *ast.CompositeLit, typ ast.Expr) {
		under := g.underlying(typ)
		var elem, val ast.Expr
		switch t := under.(type) {
		case *ast.ArrayType:
			elem = t.Elt
		case *ast.MapType:
			elem, val = t.Key, t.Value
		}
		positional := len(lit.Elts) > 0
		for _, e := range lit.Elts {
			if kv, isKV := e.(*ast.KeyValueExpr); isKV {
				positional = false
				if k, isIdent := kv.Key.(*ast.Ident); isIdent && k.Name == name && val == nil {
					set(kv.Value)
				}
			}
		}
		st, isStruct := under.(*ast.StructType)
		if positional && isStruct {
			if i := acGoFieldIndex(st, name); i >= 0 && i < len(lit.Elts) {
				set(lit.Elts[i])
			}
		} else if positional && under == nil && g.onAdded(lit, added) {
			out.undecided = true
		}
		for _, e := range lit.Elts {
			sub := elem
			if kv, isKV := e.(*ast.KeyValueExpr); isKV {
				if child, isLit := acElidedLit(kv.Key); isLit && val != nil {
					walk(child, elem)
				}
				e, sub = kv.Value, elem
				if val != nil {
					sub = val
				}
			}
			if child, isLit := acElidedLit(e); isLit && sub != nil {
				walk(child, sub)
			}
		}
	}
	ast.Inspect(g.f, func(n ast.Node) bool {
		if lit, isLit := n.(*ast.CompositeLit); isLit && lit.Type != nil {
			walk(lit, lit.Type)
		}
		return true
	})
	return out, true
}

// goFile parses p once from the branch content; nil when p is unreadable.
func (d *ACDiff) goFile(p string) *acGoFile {
	if d.gofiles == nil {
		d.gofiles = map[string]*acGoFile{}
	}
	if g, seen := d.gofiles[p]; seen {
		return g
	}
	var g *acGoFile
	if lines, ok := d.lines(p); ok {
		g = &acGoFile{fset: token.NewFileSet()}
		src := strings.Join(lines, "\n")
		if f, err := parser.ParseFile(g.fset, p, src, parser.SkipObjectResolution); err == nil {
			g.f = f
			g.types = map[string]ast.Expr{}
			// Every scope: function-local table types are common in tests.
			// A name declared twice is left unresolved, so shadowing never
			// resolves to the wrong field order.
			dup := map[string]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				if ts, isType := n.(*ast.TypeSpec); isType {
					if _, seen := g.types[ts.Name.Name]; seen {
						dup[ts.Name.Name] = true
					}
					g.types[ts.Name.Name] = ts.Type
				}
				return true
			})
			for name := range dup {
				g.types[name] = nil
			}
		}
	}
	d.gofiles[p] = g
	return g
}

// underlying resolves a literal's type expression to an inline struct,
// array or map type. nil means unresolved: a named type declared in another
// file or package, whose field order this file does not hold.
func (g *acGoFile) underlying(t ast.Expr) ast.Expr {
	for depth := 0; depth < acGoTypeDepth && t != nil; depth++ {
		switch x := t.(type) {
		case *ast.StructType, *ast.ArrayType, *ast.MapType:
			return x
		case *ast.StarExpr:
			t = x.X
		case *ast.ParenExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			t = g.types[x.Name]
		default:
			return nil
		}
	}
	return nil
}

// onAdded reports whether any of lit's elements sits on an added line.
func (g *acGoFile) onAdded(lit *ast.CompositeLit, added map[int]bool) bool {
	for _, e := range lit.Elts {
		if added[g.fset.Position(e.Pos()).Line] {
			return true
		}
	}
	return false
}

// acElidedLit returns the composite literal e is, when its type is elided
// (`{...}` or `&{...}` inside a slice, array or map literal).
func acElidedLit(e ast.Expr) (*ast.CompositeLit, bool) {
	if u, isUnary := e.(*ast.UnaryExpr); isUnary && u.Op == token.AND {
		e = u.X
	}
	lit, isLit := e.(*ast.CompositeLit)
	if !isLit || lit.Type != nil {
		return nil, false
	}
	return lit, true
}

// acGoFieldIndex is name's position in st's field order, or -1. `a, b int`
// holds two positions; an embedded field is named by its type.
func acGoFieldIndex(st *ast.StructType, name string) int {
	if st.Fields == nil {
		return -1
	}
	i := 0
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			if acEmbeddedName(f.Type) == name {
				return i
			}
			i++
			continue
		}
		for _, n := range f.Names {
			if n.Name == name {
				return i
			}
			i++
		}
	}
	return -1
}

// acEmbeddedName is an embedded field's name: its type name, through a
// pointer, a package selector or a generic instantiation.
func acEmbeddedName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StarExpr:
		return acEmbeddedName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return acEmbeddedName(x.X)
	case *ast.IndexListExpr:
		return acEmbeddedName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// acGoValueIs reports whether expression e is the span's value: a string
// literal by its unquoted or quoted text, any other expression by its source.
func acGoValueIs(e ast.Expr, want string) bool {
	if bl, isLit := e.(*ast.BasicLit); isLit && bl.Kind == token.STRING {
		if s, err := strconv.Unquote(bl.Value); err == nil && s == strings.Trim(want, `"`+"`") {
			return true
		}
		return bl.Value == want
	}
	return types.ExprString(e) == want
}
