package decompile

import (
	"slices"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

// slotRef is a register-read slot, remembering whether it is the function or
// first argument of a call (for method-call folding).
type slotRef struct {
	p    *Expr
	call *Call
	role int // 0 plain, 1 call function, 2 call first argument
	// late is set when code absorbed in an earlier pass is evaluated before
	// this slot; nothing older may be placed here without reordering.
	late bool
}

// collectRegSlots lists register reads under roots in evaluation order.
// absorbed holds expressions propagated by earlier passes.
func collectRegSlots(roots []*Expr, absorbed map[Expr]bool) []slotRef {
	var out []slotRef
	late := false
	var walk func(p *Expr, call *Call, role int)
	walk = func(p *Expr, call *Call, role int) {
		if absorbed[*p] {
			late = true
		}
		switch e := (*p).(type) {
		case *Reg:
			out = append(out, slotRef{p: p, call: call, role: role, late: late})
			return
		case *Call:
			walk(&e.Fn, e, 1)
			for i := range e.Args {
				r := 0
				if i == 0 && !e.Method {
					r = 2
				}
				walk(&e.Args[i], e, r)
			}
			return
		}
		forEachChild(*p, func(c *Expr) { walk(c, nil, 0) })
	}
	for _, r := range roots {
		walk(r, nil, 0)
	}
	return out
}

// consumerRoots returns the expressions a statement evaluates, keeping calls
// whole so method-call folding can see them.
func consumerRoots(s Stmt) []*Expr {
	if c, ok := s.(*CallStmt); ok {
		var e Expr = c.Call
		return []*Expr{&e}
	}
	return operandSlots(s)
}

// singleUse returns the def of r if r's value comes from exactly one def that
// has no other use, and that def may be propagated.
func singleUse(r *Reg) *def {
	if r.Use == nil || len(r.Use.defs) != 1 {
		return nil
	}
	d := r.Use.defs[0]
	if len(d.uses) != 1 || d.uses[0] != r.Use || d.captured || d.param || d.loop {
		return nil
	}
	return d
}

type inliner struct {
	stmts    []Stmt
	removed  []bool
	k        int
	blocked  bool
	changed  bool
	absorbed map[Expr]bool // propagated in earlier passes
	pending  []Expr        // propagated in this pass
}

// prev returns the nearest preceding statement still in place. UCLO markers
// emit no code and only matter for captured registers (never propagated), so
// they are transparent.
func (in *inliner) prev() int {
	for in.k >= 0 {
		if _, isClose := in.stmts[in.k].(*uclo); !in.removed[in.k] && !isClose {
			break
		}
		in.k--
	}
	return in.k
}

// absorb inlines the definitions immediately preceding the consumer into the
// given register slots, walking both backwards so evaluation order is kept.
func (in *inliner) absorb(slots []slotRef) {
	for i := len(slots) - 1; i >= 0 && !in.blocked; i-- {
		k := in.prev()
		if k < 0 {
			in.blocked = true
			return
		}
		sr := slots[i]
		r := (*sr.p).(*Reg)
		switch s := in.stmts[k].(type) {
		case *Assign:
			if d := singleUse(r); d != nil && !sr.late && len(s.Dst) == 1 && len(s.Src) == 1 && s.Dst[0] == Expr(d.node) {
				*sr.p = s.Src[0]
				in.take(k, s.Src[0])
				in.absorb(collectRegSlots([]*Expr{sr.p}, in.absorbed))
				continue
			}
		case *methodDef:
			if sr.role == 2 && !sr.late && i > 0 && slots[i-1].role == 1 && slots[i-1].call == sr.call && !slots[i-1].late {
				fnReg := (*slots[i-1].p).(*Reg)
				if singleUse(r) == s.Self.Def && singleUse(fnReg) == s.Fn.Def && s.Self.Def != nil && s.Fn.Def != nil {
					c := sr.call
					c.Fn = &Index{Obj: s.Obj, Key: s.Key}
					c.Args = c.Args[1:]
					c.Method = true
					in.take(k, c.Fn)
					in.absorb(collectRegSlots([]*Expr{&c.Fn}, in.absorbed))
					i-- // the function slot has been consumed too
					continue
				}
			}
		}
		// Not inlinable here. Reading another register is fine to skip past,
		// but a statement that writes this register (or anything else we
		// cannot move) ends propagation.
		if writes(in.stmts[k], r.N) {
			in.blocked = true
			return
		}
	}
}

func (in *inliner) take(k int, e Expr) {
	in.removed[k] = true
	in.changed = true
	in.k = k - 1
	in.pending = append(in.pending, e)
}

func writes(s Stmt, reg int) bool {
	for _, r := range defRegs(s) {
		if r.N == reg {
			return true
		}
	}
	return false
}

// inlineBlock propagates single-use temporaries within a block. absorbed
// carries the expressions propagated by earlier passes over the same block.
func inlineBlock(b *Block, absorbed map[Expr]bool) bool {
	in := &inliner{stmts: b.Stmts, removed: make([]bool, len(b.Stmts)), absorbed: absorbed}
	for j := 0; j <= len(b.Stmts); j++ {
		var roots []*Expr
		if j < len(b.Stmts) {
			if in.removed[j] {
				continue
			}
			roots = consumerRoots(b.Stmts[j])
		} else {
			roots = termSlots(&b.Term)
		}
		in.k, in.blocked = j-1, false
		in.absorb(collectRegSlots(roots, absorbed))
	}
	for _, e := range in.pending {
		absorbed[e] = true
	}
	if !in.changed {
		return false
	}
	kept := b.Stmts[:0]
	for i, s := range b.Stmts {
		if !in.removed[i] {
			kept = append(kept, s)
		}
	}
	clear(b.Stmts[len(kept):])
	b.Stmts = kept
	return true
}

// foldTables merges writes into a freshly created table into its constructor.
func foldTables(b *Block) bool {
	changed := false
	for k := 0; k < len(b.Stmts); k++ {
		a, ok := b.Stmts[k].(*Assign)
		if !ok || len(a.Dst) != 1 || len(a.Src) != 1 {
			continue
		}
		dst, ok := a.Dst[0].(*Reg)
		t, isTable := a.Src[0].(*Table)
		if !ok || !isTable || dst.Def == nil || t.Multi != nil {
			continue
		}
		d := dst.Def
		j := k + 1
		for ; j < len(b.Stmts); j++ {
			key, val, multi, start, obj := tableWrite(b.Stmts[j])
			if obj == nil || obj.Use == nil || len(obj.Use.defs) != 1 || obj.Use.defs[0] != d || d.captured ||
				readsDef(key, d) || readsDef(val, d) || readsDef(multi, d) ||
				(multi == nil && !fromConstructor(t, key)) {
				break
			}
			if multi != nil {
				t.Multi, t.MultiStart = multi, start
			} else {
				t.Fields = append(t.Fields, Field{Key: key, Value: val})
			}
			d.removeUse(obj.Use)
			if multi != nil {
				j++
				break // nothing can follow an open tail
			}
		}
		if j > k+1 {
			b.Stmts = append(b.Stmts[:k+1], b.Stmts[j:]...)
			changed = true
		}
	}
	return changed
}

// fromConstructor reports whether a write of key right after creating t can
// belong to the constructor. LuaJIT puts every constant key of a constructor
// into the TDUP template (with a nil placeholder for a dynamic value) and
// sizes TNEW for the constructor's array and hash parts.
func fromConstructor(t *Table, key Expr) bool {
	k, isConst := key.(*Const)
	if isConst {
		for _, f := range t.Fields {
			if fk, ok := f.Key.(*Const); ok && fk.K == k.K {
				return false // already set by the constructor
			}
		}
	}
	switch {
	case isConst && (k.K.Kind == bytecode.KString || k.K.Kind == bytecode.KHash):
		if t.tmpl == nil {
			return false // a string key would have produced a template
		}
		for _, e := range t.tmpl.Hash {
			if e.Value.Kind == bytecode.KNil && e.Key.Kind == k.K.Kind && e.Key.Str == k.K.Str && e.Key.Hash == k.K.Hash {
				return true
			}
		}
		return false
	case isConst && k.K.Kind == bytecode.KInt:
		if t.tmpl != nil {
			return k.K.Int >= 0 && k.K.Int < int64(len(t.tmpl.Array))
		}
		return k.K.Int >= 0 && k.K.Int < int64(t.hint&0x7ff)
	}
	// A computed key counts toward the hash size hint.
	return t.tmpl != nil || t.hint>>11 > 0
}

// tableWrite decomposes "R[key] = val" or a TSETM into its parts.
func tableWrite(s Stmt) (key, val, multi Expr, start int64, obj *Reg) {
	switch s := s.(type) {
	case *Assign:
		if len(s.Dst) == 1 && len(s.Src) == 1 {
			if ix, ok := s.Dst[0].(*Index); ok {
				if r, ok := ix.Obj.(*Reg); ok {
					return ix.Key, s.Src[0], nil, 0, r
				}
			}
		}
	case *tsetMulti:
		if r, ok := s.T.(*Reg); ok {
			return nil, nil, s.Vals, s.Start, r
		}
	}
	return nil, nil, nil, 0, nil
}

func readsDef(e Expr, d *def) bool {
	if e == nil {
		return false
	}
	found := false
	forEachSlot(&e, func(p *Expr) {
		if r, ok := (*p).(*Reg); ok && r.Use != nil && slices.Contains(r.Use.defs, d) {
			found = true
		}
	})
	return found
}

// propagate runs inlining and table folding to a fixed point. absorbed
// records propagated expressions across calls, so later passes never move
// older code after them.
func propagate(blocks []*Block, absorbed map[Expr]bool) {
	for _, b := range blocks {
		for inlineBlock(b, absorbed) || foldTables(b) {
		}
	}
}
