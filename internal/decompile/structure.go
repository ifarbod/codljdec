package decompile

import (
	"fmt"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

type loopKind int

const (
	loopNumFor loopKind = iota
	loopGenFor
	loopWhile
	loopRepeat
	loopForever
)

// loop describes a recovered loop over the ordered block list.
type loop struct {
	kind   loopKind
	header *Block // block at which the construct is emitted
	body   *Block // first block of the body
	latch  *Block // block holding the back edge
	exit   *Block // continuation after the loop (nil: none)
}

type structurer struct {
	fn      *Function
	order   []*Block
	idx     map[*Block]int
	loopAt  map[*Block]*loop
	active  map[*loop]bool
	labels  map[*Block]string
	used    map[string]bool
	nlabels int
}

// ctx describes the innermost enclosing loop while emitting a region.
type ctx struct {
	loop      *loop
	cont      *Block // a jump here means "continue"
	contLabel string
}

func structure(fn *Function, blocks []*Block) ([]Stmt, error) {
	s := &structurer{
		fn: fn, order: blocks, idx: map[*Block]int{}, loopAt: map[*Block]*loop{},
		active: map[*loop]bool{}, labels: map[*Block]string{}, used: map[string]bool{},
	}
	for i, b := range blocks {
		s.idx[b] = i
	}
	if err := s.findLoops(); err != nil {
		return nil, err
	}
	body := s.region(0, len(blocks), nil, &ctx{})
	return s.dropUnusedLabels(body), nil
}

func (s *structurer) findLoops() error {
	// Numeric and generic for loops are marked explicitly.
	for _, b := range s.order {
		switch b.Term.Kind {
		case TForPrep:
			s.loopAt[b] = &loop{kind: loopNumFor, header: b, body: b.Term.Then, latch: b.Term.Latch, exit: b.Term.Next}
		case TIterJump:
			latch := b.Term.Next
			if latch.Term.Kind != TIterLoop {
				return fmt.Errorf("generic for at pc %d has no ITERL", b.PC)
			}
			s.loopAt[b] = &loop{kind: loopGenFor, header: b, body: b.Term.Then, latch: latch, exit: latch.Term.Next}
		}
	}
	// Other loops come from remaining back edges; the furthest latch wins.
	latches := map[*Block]*Block{}
	for _, b := range s.order {
		if b.Term.Kind == TForLoop || b.Term.Kind == TIterLoop {
			continue
		}
		for _, t := range b.Succs() {
			if s.idx[t] <= s.idx[b] {
				if l, ok := latches[t]; !ok || s.idx[b] > s.idx[l] {
					latches[t] = b
				}
			}
		}
	}
	for h, latch := range latches {
		if _, taken := s.loopAt[h]; taken {
			return fmt.Errorf("block at pc %d heads two loops", h.PC)
		}
		hi := s.idx[latch]
		outside := func(t *Block) bool { return t != nil && s.idx[t] > hi }
		l := &loop{header: h, body: h, latch: latch}
		switch {
		case h.Term.Kind == TCond && h.Term.Then == s.at(s.idx[h]+1) && outside(h.Term.Next):
			l.kind, l.body, l.exit = loopWhile, h.Term.Then, h.Term.Next
		case h.Term.Kind == TCond && h.Term.Next == s.at(s.idx[h]+1) && outside(h.Term.Then):
			l.kind, l.body, l.exit = loopWhile, h.Term.Next, h.Term.Then
		case latch.Term.Kind == TCond && latch != h && (latch.Term.Then == h && outside(latch.Term.Next) ||
			latch.Term.Next == h && outside(latch.Term.Then)):
			l.kind = loopRepeat
			l.exit = latch.Term.Next
			if latch.Term.Next == h {
				l.exit = latch.Term.Then
			}
		default:
			l.kind = loopForever
			// The exit is the nearest block after the loop that the body jumps to.
			for _, b := range s.order[s.idx[h] : hi+1] {
				for _, t := range b.Succs() {
					if outside(t) && (l.exit == nil || s.idx[t] < s.idx[l.exit]) {
						l.exit = t
					}
				}
			}
		}
		s.loopAt[h] = l
	}
	return nil
}

func (s *structurer) at(i int) *Block {
	if i >= 0 && i < len(s.order) {
		return s.order[i]
	}
	return nil
}

func (s *structurer) label(b *Block) string {
	if l, ok := s.labels[b]; ok {
		return l
	}
	s.nlabels++
	l := fmt.Sprintf("f%d_label%d", s.fn.ID, s.nlabels)
	s.labels[b] = l
	return l
}

func (s *structurer) gotoBlock(b *Block) Stmt {
	l := s.label(b)
	s.used[l] = true
	return &Goto{Label: l}
}

// jump emits the statements for transferring control to t, given that
// control would naturally continue at next.
func (s *structurer) jump(t, next *Block, c *ctx) []Stmt {
	switch {
	case t == next:
		return nil
	case c.loop != nil && t == c.loop.exit:
		return []Stmt{&Break{}}
	case c.cont != nil && t == c.cont:
		if c.contLabel == "" {
			s.nlabels++
			c.contLabel = fmt.Sprintf("f%d_continue%d", s.fn.ID, s.nlabels)
		}
		s.used[c.contLabel] = true
		return []Stmt{&Goto{Label: c.contLabel}}
	case t == nil:
		return []Stmt{&Return{}}
	}
	return []Stmt{s.gotoBlock(t)}
}

func (s *structurer) next(j, hi int, follow *Block) *Block {
	if j < hi {
		return s.order[j]
	}
	return follow
}

// region structures blocks [lo, hi); follow is where control goes after hi.
func (s *structurer) region(lo, hi int, follow *Block, c *ctx) []Stmt {
	var out []Stmt
	for i := lo; i < hi; {
		b := s.order[i]
		if l := s.loopAt[b]; l != nil && !s.active[l] {
			out = append(out, s.emitLoop(l, c)...)
			j := s.idx[l.latch] + 1
			if l.exit != nil {
				out = append(out, s.jump(l.exit, s.next(j, hi, follow), c)...)
			}
			i = j
			continue
		}
		out = append(out, &Label{Name: s.label(b)})
		out = append(out, convertStmts(b.Stmts)...)
		nb := s.next(i+1, hi, follow)
		switch b.Term.Kind {
		case TFall, TJump:
			out = append(out, s.jump(b.Term.Next, nb, c)...)
			i++
		case TCond:
			var stmts []Stmt
			stmts, i = s.cond(b, i, hi, follow, c)
			out = append(out, stmts...)
		default: // returns and loop latches end here
			i++
		}
	}
	return out
}

func (s *structurer) cond(b *Block, i, hi int, follow *Block, c *ctx) ([]Stmt, int) {
	t := &b.Term
	nb := s.next(i+1, hi, follow)
	cond := t.Cond()
	// cx is the condition for falling into the next block; y is the other target.
	var y *Block
	var cx Expr
	switch nb {
	case t.Next:
		y, cx = t.Then, negate(cond)
	case t.Then:
		y, cx = t.Next, cond
	default:
		out := []Stmt{&If{Cond: cond, Then: s.jump(t.Then, nil, c)}}
		return append(out, s.jump(t.Next, nb, c)...), i + 1
	}
	yi, known := s.idx[y]
	if y == follow && (!known || yi >= hi) {
		return []Stmt{&If{Cond: cx, Then: s.region(i+1, hi, follow, c)}}, hi
	}
	if !known || yi <= i || yi >= hi {
		return []Stmt{&If{Cond: negate(cx), Then: s.jump(y, nil, c)}}, i + 1
	}
	// Then-branch is [i+1, yi). An unconditional forward jump at its end skips an else-branch.
	last := s.order[yi-1]
	if last.Term.Kind == TJump || last.Term.Kind == TFall {
		z := last.Term.Next
		zi, zKnown := s.idx[z]
		isElse := z != y && z != nil && (z == follow || zKnown && zi > yi && zi < hi)
		if c.loop != nil && z == c.loop.exit && z != follow {
			isElse = false // that is a break, not the end of an else-branch
		}
		if isElse {
			if z == follow {
				zi = hi
			}
			then := s.region(i+1, yi, z, c)
			els := s.region(yi, zi, z, c)
			return []Stmt{&If{Cond: cx, Then: then, Else: els}}, zi
		}
	}
	return []Stmt{&If{Cond: cx, Then: s.region(i+1, yi, y, c)}}, yi
}

func (s *structurer) emitLoop(l *loop, outer *ctx) []Stmt {
	s.active[l] = true
	c := &ctx{loop: l}
	var out []Stmt
	switch l.kind {
	case loopNumFor:
		out = append(out, &Label{Name: s.label(l.header)})
		out = append(out, convertStmts(l.header.Stmts)...)
		body := s.region(s.idx[l.body], s.idx[l.latch]+1, nil, c)
		args := l.header.Term.Args
		loopVar := loopVars(l.body)[0]
		f := &NumFor{Var: loopVar, Start: args[0], Stop: args[1], Body: body}
		if k, ok := args[2].(*Const); !ok || k.K.Kind != bytecode.KInt || k.K.Int != 1 {
			f.Step = args[2]
		}
		out = append(out, f)
	case loopGenFor:
		out = append(out, &Label{Name: s.label(l.header)})
		stmts := l.header.Stmts
		exprs := l.header.Term.Args
		// "a, b, c = explist" feeding the three iterator slots becomes the explist.
		if n := len(stmts); n > 0 {
			if a, ok := stmts[n-1].(*Assign); ok && len(a.Src) == 1 && sameVars(a.Dst, exprs) {
				exprs, stmts = a.Src, stmts[:n-1]
			}
		}
		out = append(out, convertStmts(stmts)...)
		c.cont = l.latch
		body := s.region(s.idx[l.body], s.idx[l.latch], l.latch, c)
		out = append(out, &GenFor{Vars: loopVars(l.body), Exprs: trimNils(exprs), Body: s.withContinue(body, c)})
	case loopWhile:
		c.cont = l.header
		body := s.region(s.idx[l.body], s.idx[l.latch]+1, l.header, c)
		cond := l.header.Term.Cond()
		if l.body == l.header.Term.Next {
			cond = negate(cond)
		}
		pre := convertStmts(l.header.Stmts)
		if len(pre) > 0 {
			// The condition needs statements first: loop forever and break out.
			guard := &If{Cond: negate(cond), Then: []Stmt{&Break{}}}
			body = append(append(pre, guard), body...)
			cond = &Const{K: bytecode.Const{Kind: bytecode.KTrue}}
		}
		out = append(out, &Label{Name: s.label(l.header)})
		out = append(out, &While{Cond: cond, Body: s.withContinue(body, c)})
	case loopRepeat:
		latchIdx := s.idx[l.latch]
		body := s.region(s.idx[l.header], latchIdx, l.latch, c)
		body = append(body, &Label{Name: s.label(l.latch)})
		body = append(body, convertStmts(l.latch.Stmts)...)
		until := l.latch.Term.Cond() // taken branch goes to Then
		if l.latch.Term.Then == l.header {
			until = negate(until)
		}
		out = append(out, &Repeat{Body: body, Cond: until})
	case loopForever:
		c.cont = l.header
		body := s.region(s.idx[l.header], s.idx[l.latch]+1, l.header, c)
		out = append(out, &While{Cond: &Const{K: bytecode.Const{Kind: bytecode.KTrue}}, Body: s.withContinue(body, c)})
	}
	s.active[l] = false
	return out
}

func (s *structurer) withContinue(body []Stmt, c *ctx) []Stmt {
	if c.contLabel != "" {
		body = append(body, &Label{Name: c.contLabel})
	}
	return body
}

// loopVars returns the variables defined by the loop's defStmt in its body block.
func loopVars(body *Block) []Expr {
	for _, st := range body.Stmts {
		if d, ok := st.(*defStmt); ok && d.Loop {
			var out []Expr
			for _, r := range d.Regs {
				out = append(out, &VarRef{V: defVar(r)})
			}
			return out
		}
	}
	return []Expr{&VarRef{V: &Variable{LoopVar: true}}}
}

func defVar(r *Reg) *Variable {
	if r.Def != nil && r.Def.root().variable != nil {
		return r.Def.root().variable
	}
	return &Variable{}
}

func sameVars(a, b []Expr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		va, ok1 := a[i].(*VarRef)
		vb, ok2 := b[i].(*VarRef)
		if !ok1 || !ok2 || va.V != vb.V {
			return false
		}
	}
	return true
}

// trimNils drops trailing nil constants from a generic-for explist.
func trimNils(es []Expr) []Expr {
	for len(es) > 1 {
		k, ok := es[len(es)-1].(*Const)
		if !ok || k.K.Kind != bytecode.KNil {
			break
		}
		es = es[:len(es)-1]
	}
	return es
}

// convertStmts turns IR statements into AST statements.
func convertStmts(in []Stmt) []Stmt {
	var out []Stmt
	for _, st := range in {
		switch st := st.(type) {
		case *defStmt, *uclo:
		case *tsetMulti:
			out = append(out, &Comment{Text: "TSETM outside a table constructor"})
		default:
			out = append(out, st)
		}
	}
	return out
}

// dropUnusedLabels removes labels nothing jumps to.
func (s *structurer) dropUnusedLabels(body []Stmt) []Stmt {
	var walk func([]Stmt) []Stmt
	walk = func(list []Stmt) []Stmt {
		out := list[:0]
		for _, st := range list {
			switch st := st.(type) {
			case *Label:
				if !s.used[st.Name] {
					continue
				}
			case *If:
				st.Then, st.Else = walk(st.Then), walk(st.Else)
			case *While:
				st.Body = walk(st.Body)
			case *Repeat:
				st.Body = walk(st.Body)
			case *NumFor:
				st.Body = walk(st.Body)
			case *GenFor:
				st.Body = walk(st.Body)
			}
			out = append(out, st)
		}
		return out
	}
	return walk(body)
}
