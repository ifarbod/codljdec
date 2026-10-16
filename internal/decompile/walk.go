package decompile

// forEachSlot calls fn for slot and then, recursively, for every
// sub-expression slot in Lua evaluation order. fn may replace *slot; the
// walk then descends into the replacement.
func forEachSlot(slot *Expr, fn func(*Expr)) {
	fn(slot)
	forEachChild(*slot, func(c *Expr) { forEachSlot(c, fn) })
}

// forEachChild calls fn for each direct sub-expression slot of e in evaluation order.
func forEachChild(e Expr, fn func(*Expr)) {
	switch e := e.(type) {
	case *Index:
		fn(&e.Obj)
		fn(&e.Key)
	case *Call:
		fn(&e.Fn)
		for i := range e.Args {
			fn(&e.Args[i])
		}
	case *Binop:
		fn(&e.L)
		fn(&e.R)
	case *Unop:
		fn(&e.X)
	case *Closure:
		for i := range e.Caps {
			fn(&e.Caps[i])
		}
	case *Table:
		for i := range e.Fields {
			fn(&e.Fields[i].Key)
			fn(&e.Fields[i].Value)
		}
		if e.Multi != nil {
			fn(&e.Multi)
		}
	}
}

// operandSlots returns the top-level operand slots a statement reads, in
// evaluation order. Destination registers are not operands.
func operandSlots(s Stmt) []*Expr {
	var out []*Expr
	switch s := s.(type) {
	case *Assign:
		for i := range s.Dst {
			if ix, ok := s.Dst[i].(*Index); ok {
				out = append(out, &ix.Obj, &ix.Key)
			}
		}
		for i := range s.Src {
			out = append(out, &s.Src[i])
		}
	case *CallStmt:
		out = append(out, &s.Call.Fn)
		for i := range s.Call.Args {
			out = append(out, &s.Call.Args[i])
		}
	case *Return:
		for i := range s.Vals {
			out = append(out, &s.Vals[i])
		}
	case *tsetMulti:
		out = append(out, &s.T, &s.Vals)
	case *methodDef:
		out = append(out, &s.Obj, &s.Key)
	}
	return out
}

// termSlots returns the operand slots read by a block terminator.
func termSlots(t *Term) []*Expr {
	switch t.Kind {
	case TCond:
		return []*Expr{&t.Tested}
	case TForPrep, TIterJump:
		out := make([]*Expr, len(t.Args))
		for i := range t.Args {
			out[i] = &t.Args[i]
		}
		return out
	}
	return nil
}

// defRegs returns the registers a statement writes.
func defRegs(s Stmt) []*Reg {
	switch s := s.(type) {
	case *Assign:
		var out []*Reg
		for _, d := range s.Dst {
			if r, ok := d.(*Reg); ok {
				out = append(out, r)
			}
		}
		return out
	case *defStmt:
		return s.Regs
	case *methodDef:
		return []*Reg{s.Fn, s.Self}
	}
	return nil
}

// regReads appends every register read inside the given slots, in evaluation order.
func regReads(slots []*Expr) []*Reg {
	var out []*Reg
	for _, s := range slots {
		forEachSlot(s, func(e *Expr) {
			if r, ok := (*e).(*Reg); ok {
				out = append(out, r)
			}
		})
	}
	return out
}

// hasCall reports whether e contains a call.
func hasCall(e Expr) bool {
	found := false
	forEachSlot(&e, func(s *Expr) {
		if _, ok := (*s).(*Call); ok {
			found = true
		}
	})
	return found
}

// negate returns the logical negation of a condition, simplifying where exact.
func negate(e Expr) Expr {
	switch e := e.(type) {
	case *Unop:
		if e.Op == "not" {
			return e.X
		}
	case *Binop:
		inv := map[string]string{"==": "~=", "~=": "=="}
		if op, ok := inv[e.Op]; ok {
			return &Binop{Op: op, L: e.L, R: e.R}
		}
		// Ordering comparisons: LuaJIT itself treats "not (a < b)" as "a >= b".
		ord := map[string]string{"<": ">=", ">=": "<", "<=": ">", ">": "<="}
		if op, ok := ord[e.Op]; ok {
			return &Binop{Op: op, L: e.L, R: e.R}
		}
	}
	return &Unop{Op: "not", X: e}
}
