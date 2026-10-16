package decompile

import (
	"fmt"
	"slices"
)

type frame struct {
	scope *[]Stmt
	idx   int
}

type declSite struct {
	scope *[]Stmt
	idx   int
}

// declareLocals places a declaration for every local variable at the
// innermost scope that contains all its references, then names variables in
// order of appearance.
func declareLocals(fn *Function, body *[]Stmt) {
	refs := map[*Variable][]frame{} // per variable: the common frame prefix so far
	var order []*Variable

	note := func(v *Variable, stack []frame) {
		if v.Param || v.LoopVar {
			return
		}
		prev, seen := refs[v]
		if !seen {
			refs[v] = slices.Clone(stack)
			order = append(order, v)
			return
		}
		// Keep the longest common prefix of scopes; at the deepest shared
		// scope keep the earliest statement index.
		n := 0
		for n < len(prev) && n < len(stack) && prev[n].scope == stack[n].scope {
			n++
		}
		if n < len(prev) {
			prev = prev[:n]
		}
		if n > 0 && n == len(prev) && stack[n-1].idx < prev[n-1].idx {
			prev[n-1].idx = stack[n-1].idx
		}
		refs[v] = prev
	}

	var walkList func(scope *[]Stmt, stack []frame)
	exprs := func(stack []frame, es ...Expr) {
		for _, e := range es {
			if e == nil {
				continue
			}
			forEachSlot(&e, func(p *Expr) {
				if r, ok := (*p).(*VarRef); ok {
					note(r.V, stack)
				}
			})
		}
	}
	walkList = func(scope *[]Stmt, stack []frame) {
		for i, st := range *scope {
			here := append(stack, frame{scope: scope, idx: i})
			switch st := st.(type) {
			case *Assign:
				exprs(here, st.Dst...)
				exprs(here, st.Src...)
			case *CallStmt:
				exprs(here, st.Call)
			case *Return:
				exprs(here, st.Vals...)
			case *If:
				exprs(here, st.Cond)
				walkList(&st.Then, here)
				walkList(&st.Else, here)
			case *While:
				exprs(here, st.Cond)
				walkList(&st.Body, here)
			case *Repeat:
				walkList(&st.Body, here)
				// The condition sees the body's locals.
				exprs(append(here, frame{scope: &st.Body, idx: len(st.Body)}), st.Cond)
			case *NumFor:
				exprs(here, st.Start, st.Stop, st.Step)
				walkList(&st.Body, here)
			case *GenFor:
				exprs(here, st.Exprs...)
				walkList(&st.Body, here)
			}
		}
	}
	walkList(body, nil)

	// Variables that live in the same register never overlap, so a later one
	// declared in the same scope as an earlier one is the same local reused
	// (as the source did). Captured variables keep their own binding.
	type slot struct {
		scope *[]Stmt
		reg   int
	}
	leader := map[slot]*Variable{}
	alias := map[*Variable]*Variable{}
	for _, v := range order {
		st := refs[v]
		if len(st) == 0 || v.Captured {
			continue
		}
		key := slot{st[len(st)-1].scope, v.Reg}
		if l, ok := leader[key]; ok && !l.Captured {
			alias[v] = l
		} else if !ok {
			leader[key] = v
		}
	}
	for _, p := range fn.Params {
		if l, ok := leader[slot{body, p.Reg}]; ok && !p.Captured && l != p {
			alias[l] = p
		}
	}
	for v, l := range alias {
		for alias[l] != nil {
			l = alias[l]
		}
		alias[v] = l
	}
	if len(alias) > 0 {
		renameVars(*body, alias)
	}

	// Group variables by declaration site.
	sites := map[declSite][]*Variable{}
	var siteOrder []declSite
	for _, v := range order {
		st := refs[v]
		if len(st) == 0 || alias[v] != nil {
			continue
		}
		f := st[len(st)-1]
		site := declSite{f.scope, f.idx}
		if _, ok := sites[site]; !ok {
			siteOrder = append(siteOrder, site)
		}
		sites[site] = append(sites[site], v)
	}
	// Insert from the back of each scope so indexes stay valid.
	slices.SortStableFunc(siteOrder, func(a, b declSite) int { return b.idx - a.idx })
	for _, site := range siteOrder {
		vars := sites[site]
		list := *site.scope
		var rest []*Variable
		if site.idx < len(list) {
			if a, ok := list[site.idx].(*Assign); ok && declarable(a, vars) {
				a.Local = true
				for _, v := range vars {
					if !assigns(a, v) {
						rest = append(rest, v)
					}
				}
			} else {
				rest = vars
			}
		} else {
			rest = vars
		}
		for _, v := range rest {
			v.declared = true
		}
		if len(rest) > 0 {
			list = slices.Insert(list, site.idx, Stmt(&LocalDecl{Vars: rest}))
			*site.scope = list
		}
	}
	nameVars(fn, *body)
}

// declarable reports whether a can become "local <dst> = <src>": every
// destination is a variable first declared here, and the values do not read
// them (a closure may capture itself, which prints as "local function").
func declarable(a *Assign, vars []*Variable) bool {
	for _, d := range a.Dst {
		r, ok := d.(*VarRef)
		if !ok || !slices.Contains(vars, r.V) {
			return false
		}
	}
	for _, src := range a.Src {
		if cl, ok := src.(*Closure); ok && len(a.Dst) == 1 {
			for _, c := range cl.Caps {
				if r, ok := c.(*VarRef); ok && r.V != a.Dst[0].(*VarRef).V && slices.Contains(vars, r.V) {
					return false
				}
			}
			continue
		}
		bad := false
		forEachSlot(&src, func(p *Expr) {
			if r, ok := (*p).(*VarRef); ok && slices.Contains(vars, r.V) && assigns(a, r.V) {
				bad = true
			}
		})
		if bad {
			return false
		}
	}
	return true
}

func assigns(a *Assign, v *Variable) bool {
	for _, d := range a.Dst {
		if r, ok := d.(*VarRef); ok && r.V == v {
			return true
		}
	}
	return false
}

// renameVars points references to aliased variables at their replacement.
func renameVars(list []Stmt, alias map[*Variable]*Variable) {
	refs := func(es ...Expr) {
		for _, e := range es {
			if e == nil {
				continue
			}
			forEachSlot(&e, func(p *Expr) {
				if r, ok := (*p).(*VarRef); ok && alias[r.V] != nil {
					r.V = alias[r.V]
				}
			})
		}
	}
	for _, st := range list {
		switch st := st.(type) {
		case *Assign:
			refs(st.Dst...)
			refs(st.Src...)
		case *CallStmt:
			refs(st.Call)
		case *Return:
			refs(st.Vals...)
		case *If:
			refs(st.Cond)
			renameVars(st.Then, alias)
			renameVars(st.Else, alias)
		case *While:
			refs(st.Cond)
			renameVars(st.Body, alias)
		case *Repeat:
			renameVars(st.Body, alias)
			refs(st.Cond)
		case *NumFor:
			refs(st.Start, st.Stop, st.Step)
			renameVars(st.Body, alias)
		case *GenFor:
			refs(st.Exprs...)
			renameVars(st.Body, alias)
		}
	}
}

// nameVars gives every unnamed variable a name in order of appearance.
func nameVars(fn *Function, body []Stmt) {
	n := 0
	name := func(v *Variable) {
		if v.Name == "" {
			v.Name = fmt.Sprintf("f%d_local%d", fn.ID, n)
			n++
		}
	}
	walkVars(body, name)
}

// walkVars calls fn for each variable reference in statement order.
func walkVars(list []Stmt, fn func(*Variable)) {
	exprs := func(es ...Expr) {
		for _, e := range es {
			if e == nil {
				continue
			}
			forEachSlot(&e, func(p *Expr) {
				if r, ok := (*p).(*VarRef); ok {
					fn(r.V)
				}
			})
		}
	}
	for _, st := range list {
		switch st := st.(type) {
		case *LocalDecl:
			for _, v := range st.Vars {
				fn(v)
			}
		case *Assign:
			exprs(st.Dst...)
			exprs(st.Src...)
		case *CallStmt:
			exprs(st.Call)
		case *Return:
			exprs(st.Vals...)
		case *If:
			exprs(st.Cond)
			walkVars(st.Then, fn)
			walkVars(st.Else, fn)
		case *While:
			exprs(st.Cond)
			walkVars(st.Body, fn)
		case *Repeat:
			walkVars(st.Body, fn)
			exprs(st.Cond)
		case *NumFor:
			exprs(st.Var, st.Start, st.Stop, st.Step)
			walkVars(st.Body, fn)
		case *GenFor:
			exprs(st.Vars...)
			exprs(st.Exprs...)
			walkVars(st.Body, fn)
		}
	}
}
