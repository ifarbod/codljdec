package decompile

import "math/bits"

// def is one write of a register.
type def struct {
	reg      int
	node     *Reg
	block    *Block
	uses     []*use
	param    bool      // defined on function entry
	loop     bool      // defined by a for loop
	captured bool      // the register slot is an open upvalue of a closure here
	web      *def      // union-find parent; the root identifies the variable
	variable *Variable // set on web roots by assignVars
}

// use is one read of a register.
type use struct {
	node *Reg
	defs []*def
}

// root returns the union-find representative of d's web.
func (d *def) root() *def {
	for d.web != d {
		d.web = d.web.web
		d = d.web
	}
	return d
}

func union(a, b *def) {
	ra, rb := a.root(), b.root()
	if ra != rb {
		rb.web = ra
	}
}

func (d *def) removeUse(u *use) {
	for i, x := range d.uses {
		if x == u {
			d.uses = append(d.uses[:i], d.uses[i+1:]...)
			return
		}
	}
}

type bitset []uint64

func newBitset(n int) bitset { return make(bitset, (n+63)/64) }
func (s bitset) set(i int)   { s[i/64] |= 1 << (i % 64) }
func (s bitset) clear(i int) { s[i/64] &^= 1 << (i % 64) }
func (s bitset) union(o bitset) bool { // s |= o, reports change
	changed := false
	for i, w := range o {
		if n := s[i] | w; n != s[i] {
			s[i] = n
			changed = true
		}
	}
	return changed
}

func (s bitset) each(fn func(int)) {
	for i, w := range s {
		for w != 0 {
			fn(i*64 + bits.TrailingZeros64(w))
			w &= w - 1
		}
	}
}

// analyze links every register read to its reaching definitions. It returns
// all definitions in block order.
func analyze(blocks []*Block) []*def {
	var defs []*def
	byReg := map[int][]int{} // reg -> def ids
	blockDefs := make([][]*def, len(blocks))
	for bi, b := range blocks {
		for _, s := range b.Stmts {
			var isParam, isLoop bool
			if d, ok := s.(*defStmt); ok {
				isParam, isLoop = !d.Loop, d.Loop
			}
			for _, r := range defRegs(s) {
				d := &def{reg: r.N, node: r, block: b, param: isParam, loop: isLoop}
				d.web = d
				r.Def = d
				byReg[r.N] = append(byReg[r.N], len(defs))
				defs = append(defs, d)
				blockDefs[bi] = append(blockDefs[bi], d)
			}
		}
	}
	id := make(map[*def]int, len(defs))
	for i, d := range defs {
		id[d] = i
	}

	n := len(defs)
	gen := make([]bitset, len(blocks))
	kill := make([]bitset, len(blocks))
	for bi := range blocks {
		gen[bi], kill[bi] = newBitset(n), newBitset(n)
		last := map[int]*def{}
		for _, d := range blockDefs[bi] {
			last[d.reg] = d
			for _, other := range byReg[d.reg] {
				kill[bi].set(other)
			}
		}
		for _, d := range last {
			gen[bi].set(id[d])
		}
	}

	in := make([]bitset, len(blocks))
	out := make([]bitset, len(blocks))
	for bi := range blocks {
		in[bi], out[bi] = newBitset(n), newBitset(n)
		copy(out[bi], gen[bi])
	}
	tmp := newBitset(n)
	for changed := true; changed; {
		changed = false
		for bi, b := range blocks {
			for _, p := range b.Preds {
				in[bi].union(out[p.ID])
			}
			// out = gen | (in &^ kill)
			for i := range tmp {
				tmp[i] = gen[bi][i] | (in[bi][i] &^ kill[bi][i])
			}
			if out[bi].union(tmp) {
				changed = true
			}
		}
	}

	// Walk each block linking uses.
	for bi, b := range blocks {
		cur := map[int][]*def{}
		in[bi].each(func(i int) {
			d := defs[i]
			cur[d.reg] = append(cur[d.reg], d)
		})
		link := func(slots []*Expr) {
			for _, r := range regReads(slots) {
				u := &use{node: r, defs: cur[r.N]}
				r.Use = u
				for _, d := range u.defs {
					d.uses = append(d.uses, u)
				}
			}
		}
		for _, s := range b.Stmts {
			link(operandSlots(s))
			for _, r := range defRegs(s) {
				cur[r.N] = []*def{r.Def}
			}
		}
		link(termSlots(&b.Term))
	}
	markCaptured(blocks)
	return defs
}

// markCaptured flags every definition of a register while a closure holds it
// as an open upvalue, and merges them into one variable. The upvalue stays
// open from the FNEW until a UCLO covering the register.
func markCaptured(blocks []*Block) {
	for _, b := range blocks {
		for i, s := range b.Stmts {
			for _, cap := range capturedRegs(s) {
				var anchor *def
				if cap.Use != nil && len(cap.Use.defs) > 0 {
					anchor = cap.Use.defs[0]
				}
				mark := func(d *def) {
					d.captured = true
					if anchor == nil {
						anchor = d
					}
					union(anchor, d)
				}
				if cap.Use != nil {
					for _, d := range cap.Use.defs {
						mark(d)
					}
				}
				// Writes from the capturing statement on, until the upvalue is closed.
				walkOpen(b, i, cap.N, func(d *def) { mark(d) })
			}
		}
	}
}

// capturedRegs returns the registers captured by closures created in s.
func capturedRegs(s Stmt) []*Reg {
	var out []*Reg
	for _, slot := range operandSlots(s) {
		forEachSlot(slot, func(p *Expr) {
			if c, ok := (*p).(*Closure); ok {
				for _, e := range c.Caps {
					if r, ok := e.(*Reg); ok {
						out = append(out, r)
					}
				}
			}
		})
	}
	return out
}

// walkOpen visits the defs of reg reachable from statement i of b (inclusive)
// without passing a UCLO that closes reg.
func walkOpen(b *Block, i, reg int, fn func(*def)) {
	visited := map[*Block]bool{}
	var scan func(b *Block, from int)
	scan = func(b *Block, from int) {
		for _, s := range b.Stmts[from:] {
			if u, ok := s.(*uclo); ok && u.From <= reg {
				return
			}
			for _, r := range defRegs(s) {
				if r.N == reg && r.Def != nil {
					fn(r.Def)
				}
			}
		}
		for _, succ := range b.Succs() {
			if !visited[succ] {
				visited[succ] = true
				scan(succ, 0)
			}
		}
	}
	scan(b, i)
}
