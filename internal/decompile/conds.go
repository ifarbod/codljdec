package decompile

// mergeConditions folds chains of conditional blocks into single compound
// conditions. Block b branches to b.Then when c holds, else to b.Next. When
// one target s is an empty conditional block reached only from b and the two
// share a target, the pair is one condition:
//
//	s == b.Next, s.Then == b.Then:  c or s     -> b.Then, else s.Next
//	s == b.Next, s.Next == b.Then:  c or not s -> b.Then, else s.Then
//	s == b.Then, s.Next == b.Next:  c and s     -> s.Then, else b.Next
//	s == b.Then, s.Then == b.Next:  c and not s -> s.Next, else b.Next
//
// It returns whether anything changed.
func mergeConditions(blocks []*Block) bool {
	changed := false
	for again := true; again; {
		again = false
		for _, b := range blocks {
			if b.dead || !isCond(b) || b.copyOf != nil {
				continue
			}
			if mergeOne(b) {
				again, changed = true, true
			}
		}
	}
	return changed
}

func mergeable(s, b *Block) bool {
	return s != b && isCond(s) && !s.dead && len(s.Stmts) == 0 && s.copyOf == nil &&
		len(s.Preds) == 1 && s.Preds[0] == b &&
		s.Term.Then.copyOf == nil && s.Term.Next.copyOf == nil &&
		s.Term.Then != s && s.Term.Next != s
}

func mergeOne(b *Block) bool {
	t := &b.Term
	if t.Then.copyOf != nil || t.Next.copyOf != nil {
		return false
	}
	c := t.Cond()
	var cond Expr
	var then, next, s *Block
	switch {
	case mergeable(t.Next, b):
		s = t.Next
		sc := s.Term.Cond()
		switch t.Then {
		case s.Term.Then:
			cond, then, next = &Binop{Op: "or", L: c, R: sc}, t.Then, s.Term.Next
		case s.Term.Next:
			cond, then, next = &Binop{Op: "or", L: c, R: negate(sc)}, t.Then, s.Term.Then
		}
	case mergeable(t.Then, b):
		s = t.Then
		sc := s.Term.Cond()
		switch t.Next {
		case s.Term.Next:
			cond, then, next = &Binop{Op: "and", L: c, R: sc}, s.Term.Then, t.Next
		case s.Term.Then:
			cond, then, next = &Binop{Op: "and", L: c, R: negate(sc)}, s.Term.Next, t.Next
		}
	}
	if cond == nil {
		return false
	}
	*t = Term{Kind: TCond, Tested: cond, Copy: -1, Then: then, Next: next}
	s.dead = true
	for _, n := range []*Block{then, next} {
		for i, p := range n.Preds {
			if p == s {
				n.Preds[i] = b
			}
		}
		n.Preds = dedupe(n.Preds)
	}
	return true
}

func dedupe(bs []*Block) []*Block {
	seen := map[*Block]bool{}
	out := bs[:0]
	for _, b := range bs {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}
