package decompile

import (
	"bytes"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

func newFunction(p *bytecode.Proto, file *fileCtx) *Function {
	return &Function{Proto: p, ID: file.ids[p], file: file, Vararg: p.IsVararg()}
}

// File decompiles a parsed bytecode file. Functions that fail to decompile
// are reported in their Err field and printed as commented disassembly; the
// returned error lists them.
func File(f *bytecode.File) (*Function, error) {
	ctx := &fileCtx{fr2: f.FR2(), ids: bytecode.Number(f.Main)}
	main := newFunction(f.Main, ctx)
	var failures []string
	var run func(fn *Function)
	run = func(fn *Function) {
		fn.decompile()
		if fn.Err != nil {
			failures = append(failures, fmt.Sprintf("function f%d: %v", fn.ID, fn.Err))
			return
		}
		for _, child := range closures(fn.Body) {
			run(child.Fn)
		}
	}
	run(main)
	if len(failures) > 0 {
		return main, fmt.Errorf("%d function(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return main, nil
}

func (fn *Function) decompile() {
	defer func() {
		if r := recover(); r != nil {
			fn.Err = fmt.Errorf("internal error: %v\n%s", r, debug.Stack())
		}
		if fn.Err != nil {
			fn.Body = fallbackBody(fn)
		}
	}()
	blocks, err := buildIR(fn)
	if err != nil {
		fn.Err = err
		return
	}
	blocks = mergeChains(prepareCFG(blocks))
	absorbed := map[Expr]bool{}
	for {
		analyze(blocks)
		propagate(blocks, absorbed)
		if !reduceValues(blocks) {
			break
		}
		blocks = mergeChains(reachable(blocks))
	}
	if mergeConditions(blocks) {
		blocks = mergeChains(reachable(blocks))
	}
	if err := assignVars(fn, blocks); err != nil {
		fn.Err = err
		return
	}
	body, err := structure(fn, blocks)
	if err != nil {
		fn.Err = err
		return
	}
	declareLocals(fn, &body)
	fn.Body = body
}

// fallbackBody shows the bytecode of a function that could not be decompiled.
func fallbackBody(fn *Function) []Stmt {
	var buf bytes.Buffer
	bytecode.DisassembleProto(&buf, fn.Proto, fmt.Sprintf("f%d", fn.ID))
	out := []Stmt{&Comment{Text: "bytecode:"}}
	for line := range strings.Lines(buf.String()) {
		out = append(out, &Comment{Text: strings.TrimRight(line, "\n")})
	}
	// Name parameters so the signature still prints.
	fn.Params = make([]*Variable, fn.Proto.NumParams)
	for i := range fn.Params {
		fn.Params[i] = &Variable{Name: fmt.Sprintf("f%d_arg%d", fn.ID, i), Param: true}
	}
	return out
}

// closures returns the function literals in a body, outermost first, and
// binds each child's upvalues to the parent's variables.
func closures(body []Stmt) []*Closure {
	var out []*Closure
	visit := func(e Expr) {
		if e == nil {
			return
		}
		forEachSlot(&e, func(p *Expr) {
			c, ok := (*p).(*Closure)
			if !ok {
				return
			}
			c.Fn.Upvalues = make([]*Variable, len(c.Caps))
			for i, cap := range c.Caps {
				switch cap := cap.(type) {
				case *VarRef:
					c.Fn.Upvalues[i] = cap.V
				case *Upval:
					if cap.Index < len(cap.Fn.Upvalues) {
						c.Fn.Upvalues[i] = cap.Fn.Upvalues[cap.Index]
					}
				}
			}
			out = append(out, c)
		})
	}
	var walk func([]Stmt)
	walk = func(list []Stmt) {
		for _, st := range list {
			switch st := st.(type) {
			case *Assign:
				for _, e := range st.Dst {
					visit(e)
				}
				for _, e := range st.Src {
					visit(e)
				}
			case *CallStmt:
				visit(st.Call)
			case *Return:
				for _, e := range st.Vals {
					visit(e)
				}
			case *If:
				visit(st.Cond)
				walk(st.Then)
				walk(st.Else)
			case *While:
				visit(st.Cond)
				walk(st.Body)
			case *Repeat:
				walk(st.Body)
				visit(st.Cond)
			case *NumFor:
				visit(st.Start)
				visit(st.Stop)
				visit(st.Step)
				walk(st.Body)
			case *GenFor:
				for _, e := range st.Exprs {
					visit(e)
				}
				walk(st.Body)
			}
		}
	}
	walk(body)
	return out
}
