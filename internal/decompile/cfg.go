package decompile

import "slices"

// prepareCFG materializes ISTC/ISFC copies as blocks, threads empty forward
// jumps, drops unreachable blocks and computes predecessors.
func prepareCFG(blocks []*Block) []*Block {
	blocks = materializeCopies(blocks)
	threadJumps(blocks)
	return reachable(blocks)
}

// materializeCopies replaces the taken edge of each ISTC/ISFC with a block
// that performs the copy: "if D then R = D; goto T end".
func materializeCopies(blocks []*Block) []*Block {
	out := make([]*Block, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b)
		t := &b.Term
		if t.Kind != TCond || t.Copy < 0 {
			continue
		}
		src := t.Tested.(*Reg)
		jmp := t.Then // the JMP following the test
		dest := jmp
		if len(jmp.Stmts) == 0 && jmp.Term.Kind == TJump {
			dest = jmp.Term.Next
		}
		cp := &Block{
			PC:     jmp.PC,
			Stmts:  []Stmt{&Assign{Dst: []Expr{&Reg{N: t.Copy}}, Src: []Expr{&Reg{N: src.N}}}},
			Term:   Term{Kind: TJump, Next: dest},
			copyOf: b,
		}
		t.Then = cp
		t.Copy = -1
		out = append(out, cp)
	}
	// Keep blocks ordered by pc; copy blocks sort before the JMP they replace.
	slices.SortStableFunc(out, func(a, b *Block) int { return a.PC - b.PC })
	for i, b := range out {
		b.ID = i
	}
	return out
}

// isForwarder reports whether b does nothing but jump to another block.
func isForwarder(b *Block) bool {
	return len(b.Stmts) == 0 && b.copyOf == nil && b.PC >= 0 &&
		(b.Term.Kind == TJump || b.Term.Kind == TFall) && b.Term.Next != nil && b.Term.Next != b
}

// threadJumps redirects edges that land on empty forwarding blocks.
func threadJumps(blocks []*Block) {
	resolve := func(t *Block) *Block {
		seen := map[*Block]bool{}
		for t != nil && isForwarder(t) && !seen[t] {
			seen[t] = true
			t = t.Term.Next
		}
		return t
	}
	for _, b := range blocks {
		switch b.Term.Kind {
		case TFall, TJump:
			if n := resolve(b.Term.Next); n != b.Term.Next {
				b.Term.Next = n
				b.Term.Kind = TJump
			}
		case TCond:
			b.Term.Then = resolve(b.Term.Then)
			b.Term.Next = resolve(b.Term.Next)
		}
	}
}

// mergeChains appends a block's sole successor to it when that successor has
// no other predecessor and is the very next block in code order (a jump over
// other blocks is structure, e.g. the end of a then-branch, and must stay).
// It returns the live blocks.
func mergeChains(blocks []*Block) []*Block {
	into := map[*Block]*Block{}
	nextLive := func(i int) *Block {
		for j := i + 1; j < len(blocks); j++ {
			if !blocks[j].dead {
				return blocks[j]
			}
		}
		return nil
	}
	for i, b := range blocks {
		if b.dead || b.copyOf != nil {
			continue
		}
		for b.Term.Kind == TFall || b.Term.Kind == TJump {
			s := b.Term.Next
			if s == nil || s != nextLive(i) || s.copyOf != nil || len(s.Preds) != 1 || s.Preds[0] != b {
				break
			}
			b.Stmts = append(b.Stmts, s.Stmts...)
			b.Term = s.Term
			s.dead = true
			into[s] = b
			for _, n := range b.Succs() {
				for i, p := range n.Preds {
					if p == s {
						n.Preds[i] = b
					}
				}
			}
		}
	}
	for _, b := range blocks {
		if b.Term.Kind == TForPrep {
			for into[b.Term.Latch] != nil {
				b.Term.Latch = into[b.Term.Latch]
			}
		}
	}
	return reachable(blocks)
}

// reachable drops blocks unreachable from the entry and recomputes predecessors.
func reachable(blocks []*Block) []*Block {
	seen := map[*Block]bool{}
	var visit func(*Block)
	visit = func(b *Block) {
		if b == nil || seen[b] {
			return
		}
		seen[b] = true
		for _, s := range b.Succs() {
			visit(s)
		}
		if b.Term.Kind == TForPrep {
			visit(b.Term.Latch)
		}
	}
	visit(blocks[0])
	out := blocks[:0]
	for _, b := range blocks {
		if seen[b] {
			b.Preds = nil
			out = append(out, b)
		} else {
			b.dead = true
		}
	}
	for i, b := range out {
		b.ID = i
	}
	for _, b := range out {
		for _, s := range b.Succs() {
			s.Preds = append(s.Preds, b)
		}
	}
	return out
}
