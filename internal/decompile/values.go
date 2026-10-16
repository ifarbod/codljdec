package decompile

import (
	"errors"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

// Value short-circuit reduction.
//
// LuaJIT compiles "R = a or b", "R = c and x or y", "R = a < b" and friends
// into conditional branches whose every exit stores into the same register R
// before merging at one block M. reduceValues finds such regions and replaces
// them with a single assignment of an and/or expression, when that rewrite is
// exact.

// valueRegion is a candidate region merging into m with result register r.
type valueRegion struct {
	m     *Block
	r     int
	entry *Block
	nodes map[*Block]bool
	used  map[*Block]bool
}

var errNoReduce = errors.New("not reducible")

// leafValue returns the value a leaf block stores into r, if b is a leaf
// (a single "r = e" falling into m).
func leafValue(b, m *Block, r int) (Expr, bool) {
	if len(b.Stmts) != 1 || (b.Term.Kind != TJump && b.Term.Kind != TFall) || b.Term.Next != m {
		return nil, false
	}
	a, ok := b.Stmts[0].(*Assign)
	if !ok || len(a.Dst) != 1 || len(a.Src) != 1 {
		return nil, false
	}
	d, ok := a.Dst[0].(*Reg)
	if !ok || d.N != r {
		return nil, false
	}
	return a.Src[0], true
}

// outcome is where a conditional block goes when its tested value is truthy
// (or falsy), and whether that edge just stores the tested value into r.
func (vr *valueRegion) outcome(c *Block, truthy bool) (next *Block, isCopy bool) {
	t := &c.Term
	next = t.Then
	if truthy == t.Negated {
		next = t.Next
	}
	if next.copyOf == c {
		if a := next.Stmts[0].(*Assign); a.Dst[0].(*Reg).N == vr.r && next.Term.Next == vr.m {
			return next, true
		}
	}
	if next == vr.m {
		if reg, ok := t.Tested.(*Reg); ok && reg.N == vr.r {
			return next, true // the register already holds the tested value
		}
	}
	return next, false
}

// reduceValues rewrites all reducible value regions. It reports whether
// anything changed (callers then re-run def-use analysis and propagation).
func reduceValues(blocks []*Block) bool {
	changed := false
	for _, m := range blocks {
		if m.dead || len(m.Preds) < 2 {
			continue
		}
		for _, r := range candidateRegs(m) {
			if reduceRegion(m, r) {
				changed = true
				break // m's shape changed; revisit it on the next pass
			}
		}
	}
	return changed
}

// candidateRegs lists registers stored by more than one predecessor leaf of m.
func candidateRegs(m *Block) []int {
	count := map[int]int{}
	var out []int
	for _, p := range m.Preds {
		if p.dead || len(p.Stmts) != 1 {
			continue
		}
		if a, ok := p.Stmts[0].(*Assign); ok && len(a.Dst) == 1 {
			if d, ok := a.Dst[0].(*Reg); ok {
				if count[d.N]++; count[d.N] == 1 {
					out = append(out, d.N)
				}
			}
		}
	}
	return out
}

func isCond(b *Block) bool { return b.Term.Kind == TCond }

func reduceRegion(m *Block, r int) bool {
	vr := &valueRegion{m: m, r: r, nodes: map[*Block]bool{}, used: map[*Block]bool{}}
	// Seed with leaves storing r into m.
	for _, p := range m.Preds {
		if _, ok := leafValue(p, m, r); ok && !p.dead {
			vr.nodes[p] = true
		}
	}
	// Grow backwards through conditional blocks whose outcomes all stay in the region.
	inRegion := func(c *Block, truthy bool) bool {
		n, isCopy := vr.outcome(c, truthy)
		return isCopy || vr.nodes[n]
	}
	for grew := true; grew; {
		grew = false
		for n := range vr.nodes {
			for _, p := range n.Preds {
				if !vr.nodes[p] && !p.dead && isCond(p) && inRegion(p, true) && inRegion(p, false) {
					vr.nodes[p] = true
					grew = true
				}
			}
		}
		// Conditions jumping straight to m with an implicit copy.
		for _, p := range m.Preds {
			if !vr.nodes[p] && !p.dead && isCond(p) && inRegion(p, true) && inRegion(p, false) {
				vr.nodes[p] = true
				grew = true
			}
		}
	}
	// Copy blocks of member conditions are part of the region too.
	for n := range vr.nodes {
		if isCond(n) {
			for _, s := range n.Succs() {
				if s.copyOf == n {
					vr.nodes[s] = true
				}
			}
		}
	}
	// Exactly one entry, which must be a condition; everything else is internal.
	var entry *Block
	for n := range vr.nodes {
		external := len(n.Preds) == 0 // the function entry
		for _, p := range n.Preds {
			external = external || !vr.nodes[p]
		}
		if external {
			if entry != nil && entry != n {
				return false
			}
			entry = n
		}
	}
	if entry == nil || !isCond(entry) {
		return false
	}
	vr.entry = entry
	for n := range vr.nodes {
		if n == entry {
			continue
		}
		if _, ok := vr.testValue(n, true); isCond(n) && !ok {
			return false
		}
		for _, s := range n.Succs() {
			if s.PC <= n.PC && s.copyOf == nil && n.copyOf == nil {
				return false // a back edge: this is a loop, not an expression
			}
		}
	}

	e, err := vr.build(entry)
	if err != nil {
		return false
	}
	// Every region node must appear in the expression (each at most once,
	// which build enforces).
	for n := range vr.nodes {
		if !vr.used[n] {
			return false
		}
	}
	entry.Stmts = append(entry.Stmts, &Assign{Dst: []Expr{&Reg{N: r}}, Src: []Expr{e}})
	entry.Term = Term{Kind: TJump, Next: m}
	preds := []*Block{entry}
	for _, p := range m.Preds {
		if !vr.nodes[p] {
			preds = append(preds, p)
		}
	}
	m.Preds = preds
	for n := range vr.nodes {
		if n != entry {
			n.dead = true
		}
	}
	return true
}

// use marks a node as consumed; each may appear only once in the result,
// except constant leaves, whose duplication has no effect.
func (vr *valueRegion) use(n *Block) error {
	if vr.used[n] {
		if v, ok := leafValue(n, vr.m, vr.r); !ok || isCond(n) || !isConstExpr(v) {
			return errNoReduce
		}
	}
	vr.used[n] = true
	return nil
}

func isConstExpr(e Expr) bool {
	_, ok := e.(*Const)
	return ok
}

// andOr builds "(c and e) or y", dropping "and true" after a boolean.
func andOr(c, e, y Expr) Expr {
	if isBool(c) && isConstKind(e, bytecode.KTrue) {
		return &Binop{Op: "or", L: c, R: y}
	}
	return &Binop{Op: "or", L: &Binop{Op: "and", L: c, R: e}, R: y}
}

// testValue returns the value a region condition tests. An inner condition
// may first store into r and test r ("R = e; IST R"), which tests e.
func (vr *valueRegion) testValue(n *Block, inner bool) (Expr, bool) {
	if !inner || len(n.Stmts) == 0 {
		return n.Term.Tested, true
	}
	if len(n.Stmts) != 1 {
		return nil, false
	}
	a, ok := n.Stmts[0].(*Assign)
	if !ok || len(a.Dst) != 1 || len(a.Src) != 1 {
		return nil, false
	}
	d, ok := a.Dst[0].(*Reg)
	t, ok2 := n.Term.Tested.(*Reg)
	if !ok || !ok2 || d.N != vr.r || t.N != vr.r {
		return nil, false
	}
	return a.Src[0], true
}

// build returns the value stored into r when control reaches n.
func (vr *valueRegion) build(n *Block) (Expr, error) {
	if err := vr.use(n); err != nil {
		return nil, err
	}
	if v, ok := leafValue(n, vr.m, vr.r); ok && !isCond(n) {
		return v, nil
	}
	if !isCond(n) || !vr.nodes[n] {
		return nil, errNoReduce
	}
	v, ok := vr.testValue(n, n != vr.entry)
	if !ok {
		return nil, errNoReduce
	}
	a, aCopy := vr.outcome(n, true)
	b, bCopy := vr.outcome(n, false)
	vr.markCopy(a, aCopy)
	vr.markCopy(b, bCopy)
	switch {
	case aCopy && bCopy:
		return v, nil
	case aCopy: // truthy: keep v; falsy: compute the alternative
		y, err := vr.build(b)
		if err != nil {
			return nil, err
		}
		return &Binop{Op: "or", L: v, R: y}, nil
	case bCopy:
		x, err := vr.build(a)
		if err != nil {
			return nil, err
		}
		return &Binop{Op: "and", L: v, R: x}, nil
	}
	// Both outcomes compute a value: v ? X : Y. Two plain values first.
	if !isCond(a) && !isCond(b) {
		x, errX := vr.build(a)
		y, errY := vr.build(b)
		if errX != nil || errY != nil {
			return nil, errNoReduce
		}
		if e := ternary(v, x, y); e != nil {
			return e, nil
		}
		return nil, errNoReduce
	}
	// "(v and E) or Y" where E's falsy outcome also leads to Y, or the mirror
	// "(not v and E) or X" when the falsy side holds the and-chain.
	for _, try := range []struct {
		cond      Expr
		and, rest *Block
	}{{v, a, b}, {negate(v), b, a}} {
		saved := vr.snapshot()
		e, err := vr.buildAnd(try.and, try.rest)
		if err != nil {
			vr.restore(saved)
			continue
		}
		y, err := vr.build(try.rest)
		if err != nil {
			vr.restore(saved)
			continue
		}
		return andOr(try.cond, e, y), nil
	}
	x, errX := vr.build(a)
	y, errY := vr.build(b)
	if errX != nil || errY != nil {
		return nil, errNoReduce
	}
	if e := ternary(v, x, y); e != nil {
		return e, nil
	}
	return nil, errNoReduce
}

// ternary expresses "if v then x else y" exactly, or returns nil.
func ternary(v, x, y Expr) Expr {
	switch {
	case isBool(v) && isConstKind(x, bytecode.KTrue) && isConstKind(y, bytecode.KFalse):
		return v
	case isBool(v) && isConstKind(x, bytecode.KFalse) && isConstKind(y, bytecode.KTrue):
		return negate(v)
	case isConstKind(y, bytecode.KTrue): // v falsy gives "not v" == true
		return &Binop{Op: "or", L: negate(v), R: x}
	case isConstKind(x, bytecode.KFalse): // v truthy gives "not v" == false
		return &Binop{Op: "and", L: negate(v), R: y}
	case isBool(v) && isConstKind(x, bytecode.KTrue):
		return &Binop{Op: "or", L: v, R: y}
	case isBool(v) && isConstKind(y, bytecode.KFalse):
		return &Binop{Op: "and", L: v, R: x}
	case alwaysTruthy(x):
		return andOr(v, x, y)
	}
	return nil
}

func (vr *valueRegion) markCopy(n *Block, isCopy bool) {
	if isCopy && n != vr.m {
		vr.used[n] = true
	}
}

// buildAnd returns E such that running from n stores E into r when E is
// truthy, and otherwise transfers to fail without storing.
func (vr *valueRegion) buildAnd(n, fail *Block) (Expr, error) {
	if n == fail || (isCond(n) && vr.used[n]) {
		return nil, errNoReduce
	}
	if !isCond(n) {
		v, ok := leafValue(n, vr.m, vr.r)
		if !ok || !alwaysTruthy(v) || vr.use(n) != nil {
			return nil, errNoReduce
		}
		return v, nil
	}
	if !vr.nodes[n] {
		return nil, errNoReduce
	}
	w, ok := vr.testValue(n, true)
	if !ok {
		return nil, errNoReduce
	}
	a, aCopy := vr.outcome(n, true)
	b, bCopy := vr.outcome(n, false)
	switch {
	case b == fail && !bCopy && aCopy:
		vr.used[n] = true
		vr.markCopy(a, true)
		return w, nil
	case b == fail && !bCopy:
		saved := vr.snapshot()
		vr.used[n] = true
		e, err := vr.buildAnd(a, fail)
		if err != nil {
			vr.restore(saved)
			return nil, err
		}
		return &Binop{Op: "and", L: w, R: e}, nil
	case aCopy && !bCopy:
		saved := vr.snapshot()
		vr.used[n] = true
		vr.markCopy(a, true)
		e, err := vr.buildAnd(b, fail)
		if err != nil {
			vr.restore(saved)
			return nil, err
		}
		return &Binop{Op: "or", L: w, R: e}, nil
	}
	return nil, errNoReduce
}

func (vr *valueRegion) snapshot() map[*Block]bool {
	s := make(map[*Block]bool, len(vr.used))
	for k, v := range vr.used {
		s[k] = v
	}
	return s
}

func (vr *valueRegion) restore(s map[*Block]bool) { vr.used = s }

func isConstKind(e Expr, k bytecode.ConstKind) bool {
	c, ok := e.(*Const)
	return ok && c.K.Kind == k
}

// isBool reports whether e always evaluates to a boolean.
func isBool(e Expr) bool {
	switch e := e.(type) {
	case *Binop:
		switch e.Op {
		case "==", "~=", "<", "<=", ">", ">=":
			return true
		}
	case *Unop:
		return e.Op == "not"
	case *Const:
		return e.K.Kind == bytecode.KTrue || e.K.Kind == bytecode.KFalse
	}
	return false
}

// alwaysTruthy reports whether e can never evaluate to nil or false.
// Operators are excluded: a metamethod may return anything.
func alwaysTruthy(e Expr) bool {
	switch e := e.(type) {
	case *Const:
		return e.K.Kind != bytecode.KNil && e.K.Kind != bytecode.KFalse
	case *Table, *Closure:
		return true
	}
	return false
}
