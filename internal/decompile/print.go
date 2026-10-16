package decompile

import (
	"fmt"
	"io"
	"strings"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

var keywords = map[string]bool{
	"and": true, "break": true, "do": true, "else": true, "elseif": true, "end": true,
	"false": true, "for": true, "function": true, "goto": true, "if": true, "in": true,
	"local": true, "nil": true, "not": true, "or": true, "repeat": true, "return": true,
	"then": true, "true": true, "until": true, "while": true,
}

func isIdent(s string) bool {
	if s == "" || keywords[s] {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Operator precedence (higher binds tighter) and right-associativity.
var binPrec = map[string]int{
	"or": 1, "and": 2,
	"<": 3, ">": 3, "<=": 3, ">=": 3, "~=": 3, "==": 3,
	"..": 4, "+": 5, "-": 5, "*": 6, "/": 6, "%": 6, "^": 8,
}

const unaryPrec = 7

type printer struct {
	w      strings.Builder
	indent int
}

// Print writes the main function's body as a Lua chunk.
func Print(w io.Writer, fn *Function) error {
	p := &printer{}
	p.body(fn)
	_, err := io.WriteString(w, p.w.String())
	return err
}

func (p *printer) line(format string, args ...any) {
	p.w.WriteString(strings.Repeat("\t", p.indent))
	fmt.Fprintf(&p.w, format, args...)
	p.w.WriteByte('\n')
}

func (p *printer) body(fn *Function) {
	if fn.Err != nil {
		p.line("-- decompilation failed: %s", strings.ReplaceAll(fn.Err.Error(), "\n", " "))
	}
	body := fn.Body
	// A trailing bare return is implicit.
	if n := len(body); n > 0 {
		if r, ok := body[n-1].(*Return); ok && len(r.Vals) == 0 {
			body = body[:n-1]
		}
	}
	p.block(body)
}

func (p *printer) block(list []Stmt) {
	for i, st := range list {
		// "return" must end a block; wrap it when something (a label) follows.
		switch st.(type) {
		case *Return, *Break:
			if i < len(list)-1 {
				p.line("do")
				p.nested([]Stmt{st})
				p.line("end")
				continue
			}
		}
		p.stmt(st)
	}
}

func (p *printer) nested(list []Stmt) {
	p.indent++
	p.block(list)
	p.indent--
}

func (p *printer) stmt(st Stmt) {
	switch st := st.(type) {
	case *Assign:
		p.assign(st)
	case *LocalDecl:
		names := make([]string, len(st.Vars))
		for i, v := range st.Vars {
			names[i] = v.Name
		}
		p.line("local %s", strings.Join(names, ", "))
	case *CallStmt:
		p.lineGuarded(p.expr(st.Call, 0))
	case *Return:
		if len(st.Vals) == 0 {
			p.line("return")
		} else {
			p.line("return %s", p.exprList(st.Vals, true))
		}
	case *If:
		p.ifChain(st)
	case *While:
		p.line("while %s do", p.expr(st.Cond, 0))
		p.nested(st.Body)
		p.line("end")
	case *Repeat:
		p.line("repeat")
		p.nested(st.Body)
		p.line("until %s", p.expr(st.Cond, 0))
	case *NumFor:
		head := fmt.Sprintf("for %s = %s, %s", p.expr(st.Var, 0), p.expr(st.Start, 0), p.expr(st.Stop, 0))
		if st.Step != nil {
			head += ", " + p.expr(st.Step, 0)
		}
		p.line("%s do", head)
		p.nested(st.Body)
		p.line("end")
	case *GenFor:
		p.line("for %s in %s do", p.exprList(st.Vars, false), p.exprList(st.Exprs, true))
		p.nested(st.Body)
		p.line("end")
	case *Break:
		p.line("break")
	case *Goto:
		p.line("goto %s", st.Label)
	case *Label:
		p.line("::%s::", st.Name)
	case *Comment:
		p.line("-- %s", st.Text)
	}
}

// lineGuarded writes a statement, prefixing ";" when it starts with "(" so it
// cannot be parsed as a call continuing the previous line.
func (p *printer) lineGuarded(s string) {
	if strings.HasPrefix(s, "(") {
		s = ";" + s
	}
	p.line("%s", s)
}

func (p *printer) assign(a *Assign) {
	if a.Local && len(a.Dst) == 1 && len(a.Src) == 1 {
		if cl, ok := a.Src[0].(*Closure); ok && capturesSelf(cl, a.Dst[0]) {
			p.function("local function "+p.expr(a.Dst[0], 0), cl.Fn, "")
			return
		}
	}
	prefix := ""
	if a.Local {
		prefix = "local "
	}
	dst := p.exprList(a.Dst, false)
	if a.Local && len(a.Src) == 1 {
		if k, ok := a.Src[0].(*Const); ok && k.K.Kind == bytecode.KNil {
			p.line("local %s", dst)
			return
		}
	}
	// Only a value list shorter than the targets expands its last call.
	p.lineGuarded(prefix + dst + " = " + p.exprList(a.Src, len(a.Dst) > len(a.Src)))
}

func capturesSelf(cl *Closure, dst Expr) bool {
	d, ok := dst.(*VarRef)
	if !ok {
		return false
	}
	for _, c := range cl.Caps {
		if r, ok := c.(*VarRef); ok && r.V == d.V {
			return true
		}
	}
	return false
}

func (p *printer) ifChain(st *If) {
	p.line("if %s then", p.expr(st.Cond, 0))
	for {
		p.nested(st.Then)
		if len(st.Else) == 1 {
			if next, ok := st.Else[0].(*If); ok {
				p.line("elseif %s then", p.expr(next.Cond, 0))
				st = next
				continue
			}
		}
		if len(st.Else) > 0 {
			p.line("else")
			p.nested(st.Else)
		}
		p.line("end")
		return
	}
}

// function writes "<head>(params) body end"; suffix follows "end".
func (p *printer) function(head string, fn *Function, suffix string) {
	params := make([]string, 0, len(fn.Params)+1)
	for _, v := range fn.Params {
		params = append(params, v.Name)
	}
	if fn.Proto.IsVararg() {
		params = append(params, "...")
	}
	p.line("%s(%s)", head, strings.Join(params, ", "))
	p.indent++
	p.body(fn)
	p.indent--
	p.line("end%s", suffix)
}

// exprList renders a comma-separated list; if multi, the last expression is
// in a position that expands to all its values.
func (p *printer) exprList(es []Expr, multi bool) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = p.expr(e, 0)
		if multi && i == len(es)-1 && singleValued(e) {
			parts[i] = "(" + parts[i] + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// singleValued reports whether e is a call or vararg truncated to one value,
// which needs parentheses where Lua would otherwise expand it.
func singleValued(e Expr) bool {
	switch e := e.(type) {
	case *Call:
		return !e.Multi
	case *Vararg:
		return !e.Multi
	}
	return false
}

func (p *printer) constant(k bytecode.Const) string { return k.String() }

func (p *printer) name(e Expr) string {
	switch e := e.(type) {
	case *VarRef:
		if e.V.Name == "" {
			return "unnamed"
		}
		return e.V.Name
	case *Upval:
		if e.Index < len(e.Fn.Upvalues) && e.Fn.Upvalues[e.Index] != nil && e.Fn.Upvalues[e.Index].Name != "" {
			return e.Fn.Upvalues[e.Index].Name
		}
		return fmt.Sprintf("f%d_upvalue%d", e.Fn.ID, e.Index)
	case *Global:
		if e.Key.Kind == bytecode.KString && isIdent(e.Key.Str) {
			return e.Key.Str
		}
		return "_G[" + p.constant(e.Key) + "]"
	}
	return ""
}

// prefix renders e so it can be called or indexed.
func (p *printer) prefix(e Expr) string {
	switch e.(type) {
	case *VarRef, *Upval, *Global, *Index, *Call:
		return p.expr(e, 0)
	}
	return "(" + p.expr(e, 0) + ")"
}

func (p *printer) key(obj Expr, key Expr) string {
	if k, ok := key.(*Const); ok && k.K.Kind == bytecode.KString && isIdent(k.K.Str) {
		return p.prefix(obj) + "." + k.K.Str
	}
	return p.prefix(obj) + "[" + p.expr(key, 0) + "]"
}

func (p *printer) expr(e Expr, prec int) string {
	switch e := e.(type) {
	case *VarRef, *Upval, *Global:
		return p.name(e)
	case *Const:
		return p.constant(e.K)
	case *Vararg:
		return "..."
	case *Index:
		return p.key(e.Obj, e.Key)
	case *Call:
		args := p.exprList(e.Args, true)
		if e.Method {
			ix := e.Fn.(*Index)
			if k, ok := ix.Key.(*Const); ok && k.K.Kind == bytecode.KString && isIdent(k.K.Str) {
				return fmt.Sprintf("%s:%s(%s)", p.prefix(ix.Obj), k.K.Str, args)
			}
			// No method syntax for this key: pass the receiver explicitly.
			all := append([]Expr{ix.Obj}, e.Args...)
			return fmt.Sprintf("%s(%s)", p.key(ix.Obj, ix.Key), p.exprList(all, true))
		}
		return fmt.Sprintf("%s(%s)", p.prefix(e.Fn), args)
	case *Binop:
		bp := binPrec[e.Op]
		right := e.Op == ".." || e.Op == "^"
		lp, rp := bp, bp+1
		if right {
			lp, rp = bp+1, bp
		}
		s := p.expr(e.L, lp) + " " + e.Op + " " + p.expr(e.R, rp)
		if bp < prec {
			return "(" + s + ")"
		}
		return s
	case *Unop:
		x := p.expr(e.X, unaryPrec)
		sep := ""
		if e.Op == "not" {
			sep = " "
		} else if strings.HasPrefix(x, "-") {
			x = "(" + x + ")"
		}
		s := e.Op + sep + x
		if unaryPrec < prec {
			return "(" + s + ")"
		}
		return s
	case *Closure:
		return p.closure(e)
	case *Table:
		return p.table(e)
	case *MultRes:
		return "--[[MULTRES]]"
	case *Reg:
		return fmt.Sprintf("--[[r%d]]", e.N)
	}
	return fmt.Sprintf("--[[%T]]", e)
}

// closure renders a function literal; its body is printed at the current
// indentation inside a sub-printer and spliced in.
func (p *printer) closure(c *Closure) string {
	sub := &printer{indent: p.indent}
	sub.function("function ", c.Fn, "")
	s := strings.TrimSuffix(sub.w.String(), "\n")
	return strings.TrimLeft(s, "\t")
}

func (p *printer) table(t *Table) string {
	if len(t.Fields) == 0 && t.Multi == nil {
		return "{}"
	}
	sub := &printer{indent: p.indent + 1}
	var items []string
	next := int64(1)
	for _, f := range t.Fields {
		if k, ok := f.Key.(*Const); ok && k.K.Kind == bytecode.KInt && k.K.Int == next {
			items = append(items, sub.expr(f.Value, 0))
			next++
			continue
		}
		if k, ok := f.Key.(*Const); ok && k.K.Kind == bytecode.KString && isIdent(k.K.Str) {
			items = append(items, k.K.Str+" = "+sub.expr(f.Value, 0))
			continue
		}
		items = append(items, "["+sub.expr(f.Key, 0)+"] = "+sub.expr(f.Value, 0))
	}
	if t.Multi != nil {
		items = append(items, sub.expr(t.Multi, 0))
	}
	one := "{ " + strings.Join(items, ", ") + " }"
	if len(one) <= 80 && !strings.Contains(one, "\n") {
		return one
	}
	ind := strings.Repeat("\t", p.indent+1)
	return "{\n" + ind + strings.Join(items, ",\n"+ind) + "\n" + strings.Repeat("\t", p.indent) + "}"
}
