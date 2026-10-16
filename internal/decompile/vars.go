package decompile

import "fmt"

// assignVars groups definitions connected through shared uses into variables
// and rewrites every register in the IR into a VarRef.
func assignVars(fn *Function, blocks []*Block) error {
	defs := analyze(blocks)
	for _, d := range defs {
		for _, u := range d.uses {
			for _, other := range u.defs {
				union(d, other)
			}
		}
	}

	varOf := func(d *def) *Variable {
		r := d.root()
		if r.variable == nil {
			r.variable = &Variable{Reg: r.reg}
		}
		if d.captured {
			r.variable.Captured = true
		}
		if d.loop {
			r.variable.LoopVar = true
		}
		return r.variable
	}
	for _, d := range defs {
		varOf(d)
	}

	// Parameters are the defs of the synthetic entry block.
	entry := defRegs(blocks[0].Stmts[0])
	fn.Params = make([]*Variable, len(entry))
	for i, r := range entry {
		v := varOf(r.Def)
		v.Param, v.Name = true, fmt.Sprintf("f%d_arg%d", fn.ID, i)
		fn.Params[i] = v
	}

	var err error
	toVar := func(p *Expr) {
		r, ok := (*p).(*Reg)
		if !ok {
			return
		}
		if r.N == regMultRes && err == nil {
			err = fmt.Errorf("open multiple results were not consumed")
		}
		*p = &VarRef{V: regVar(r, varOf)}
	}
	rewrite := func(slot *Expr) { forEachSlot(slot, toVar) }

	for _, b := range blocks {
		lowered := b.Stmts[:0:0]
		for _, s := range b.Stmts {
			if m, ok := s.(*methodDef); ok {
				// An unfolded method prologue: evaluate the receiver once.
				self := &Assign{Dst: []Expr{m.Self}, Src: []Expr{m.Obj}}
				fnv := &Assign{Dst: []Expr{m.Fn}, Src: []Expr{&Index{Obj: &Reg{N: m.Self.N, Use: &use{defs: []*def{m.Self.Def}}}, Key: m.Key}}}
				lowered = append(lowered, self, fnv)
				continue
			}
			lowered = append(lowered, s)
		}
		b.Stmts = lowered
		for _, s := range b.Stmts {
			if a, ok := s.(*Assign); ok {
				for i := range a.Dst {
					if _, isReg := a.Dst[i].(*Reg); isReg {
						toVar(&a.Dst[i])
					} else {
						rewrite(&a.Dst[i])
					}
				}
			}
			for _, slot := range operandSlots(s) {
				rewrite(slot)
			}
		}
		for _, slot := range termSlots(&b.Term) {
			rewrite(slot)
		}
	}
	return err
}

// regVar returns the variable a register node refers to.
func regVar(r *Reg, varOf func(*def) *Variable) *Variable {
	switch {
	case r.Def != nil:
		return varOf(r.Def)
	case r.Use != nil && len(r.Use.defs) > 0:
		return varOf(r.Use.defs[0])
	}
	return &Variable{} // read of a never-written register
}
