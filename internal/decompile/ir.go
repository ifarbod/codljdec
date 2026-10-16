package decompile

import (
	"fmt"
	"math"
	"slices"

	"github.com/ifarbod/codljdec/internal/bytecode"
)

// regMultRes is a pseudo register holding the open results of a call or vararg.
const regMultRes = -1

// Block is a basic block of IR statements.
type Block struct {
	ID    int
	PC    int // first bytecode pc (blocks are ordered by it)
	Stmts []Stmt
	Term  Term
	Preds []*Block
	dead  bool
	label string // goto label, assigned on demand
	// copyOf is set on blocks synthesized for an ISTC/ISFC copy; it points at
	// the conditional block whose taken edge the copy sits on.
	copyOf *Block
}

// TermKind enumerates block terminators.
type TermKind int

const (
	TFall     TermKind = iota // continue at Next
	TJump                     // unconditional jump to Next
	TCond                     // if Cond then Then else Next
	TReturn                   // no successors (last stmt is a Return)
	TForPrep                  // numeric for setup: body is Then, skip is Next
	TForLoop                  // numeric for latch: back to Then, exit to Next
	TIterJump                 // generic for setup: jumps to the iterator block Next
	TIterLoop                 // generic for latch: back to Then, exit to Next
)

// Term is a block terminator.
type Term struct {
	Kind TermKind
	Then *Block // conditional / loop-back target
	Next *Block // fallthrough / jump target / loop exit
	// TCond: the branch to Then is taken when Tested is truthy (or falsy if Negated).
	// Comparisons are encoded as a Tested Binop.
	Tested  Expr
	Negated bool
	// ISTC/ISFC: when the branch is taken, register Copy receives the tested value.
	Copy int
	// Loop operands: numeric for reads Args = start, stop, step; generic for reads
	// Args = generator, state, control. Base is the first loop register.
	Args []Expr
	Base int
	NRes int // generic for: number of loop variables
	// TForPrep: the block ending in the matching FORL (kept even if unreachable,
	// e.g. when the body always returns).
	Latch *Block
}

// Cond returns the expression under which the Then branch is taken.
func (t *Term) Cond() Expr {
	if t.Negated {
		return negate(t.Tested)
	}
	return t.Tested
}

// Succs returns the successor blocks.
func (b *Block) Succs() []*Block {
	switch b.Term.Kind {
	case TReturn:
		return nil
	case TFall, TJump, TIterJump:
		return []*Block{b.Term.Next}
	}
	return []*Block{b.Term.Then, b.Term.Next}
}

// Internal IR statements.
type (
	// defStmt defines registers without code: parameters and loop variables.
	defStmt struct {
		Regs []*Reg
		Loop bool
	}
	// tsetMulti is TSETM: T[Start], T[Start+1], ... = Vals...
	tsetMulti struct {
		T     Expr
		Start int64
		Vals  Expr
	}
	// methodDef loads a method and its receiver: Fn = Obj[Key]; Self = Obj.
	methodDef struct {
		Fn, Self *Reg
		Obj, Key Expr
	}
	// uclo closes upvalues of registers >= From (scope end); it emits no code.
	uclo struct{ From int }
)

func (*defStmt) isStmt()   {}
func (*tsetMulti) isStmt() {}
func (*methodDef) isStmt() {}
func (*uclo) isStmt()      {}

type irBuilder struct {
	fn     *Function
	p      *bytecode.Proto
	fr2    int
	blocks map[int]*Block // by start pc
}

func (b *irBuilder) reg(n int) *Reg { return &Reg{N: n} }

func (b *irBuilder) regs(from, to int) []Expr {
	var out []Expr
	for n := from; n <= to; n++ {
		out = append(out, b.reg(n))
	}
	return out
}

func (b *irBuilder) kgc(i int) bytecode.Const {
	if i < len(b.p.KGC) {
		return b.p.KGC[i]
	}
	return bytecode.Const{Kind: bytecode.KNil}
}

func (b *irBuilder) kn(i int) Expr {
	if i < len(b.p.KN) {
		return &Const{K: b.p.KN[i]}
	}
	return &Const{K: bytecode.Const{Kind: bytecode.KNil}}
}

func pri(v int) Expr {
	return &Const{K: bytecode.Const{Kind: [...]bytecode.ConstKind{bytecode.KNil, bytecode.KFalse, bytecode.KTrue}[min(v, 2)]}}
}

func intConst(v int64) Expr { return &Const{K: bytecode.Const{Kind: bytecode.KInt, Int: v}} }

// strKey converts a GC constant used as a table key or global name into an expression.
func (b *irBuilder) strKey(i int) Expr { return &Const{K: b.kgc(i)} }

// buildIR translates a prototype into basic blocks. The first block is a
// synthetic entry holding the parameter definitions.
func buildIR(fn *Function) ([]*Block, error) {
	p := fn.Proto
	b := &irBuilder{fn: fn, p: p, blocks: map[int]*Block{}}
	if fn.file.fr2 {
		b.fr2 = 1
	}
	code := p.Code
	n := len(code)

	target := func(pc int) int { return pc + 1 + code[pc].SD() }

	// Find block leaders.
	leaders := map[int]bool{0: true}
	for pc, ins := range code {
		switch ins.Op {
		case bytecode.JMP, bytecode.ISNEXT, bytecode.FORI, bytecode.FORL:
			leaders[target(pc)] = true
			leaders[pc+1] = true
		case bytecode.UCLO:
			if ins.D != 0x8000 {
				leaders[target(pc)] = true
				leaders[pc+1] = true
			}
		case bytecode.ITERL:
			leaders[target(pc)] = true
			leaders[pc+1] = true
		case bytecode.ITERC, bytecode.ITERN:
			leaders[pc] = true // the latch block is ITERC + ITERL
		case bytecode.ISLT, bytecode.ISGE, bytecode.ISLE, bytecode.ISGT, bytecode.ISEQV, bytecode.ISNEV,
			bytecode.ISEQS, bytecode.ISNES, bytecode.ISEQN, bytecode.ISNEN, bytecode.ISEQP, bytecode.ISNEP,
			bytecode.ISTC, bytecode.ISFC, bytecode.IST, bytecode.ISF:
			leaders[pc+1] = true // the following JMP is its own block
			leaders[pc+2] = true
		case bytecode.RET, bytecode.RET0, bytecode.RET1, bytecode.RETM, bytecode.CALLT, bytecode.CALLMT:
			leaders[pc+1] = true
		}
	}
	starts := make([]int, 0, len(leaders))
	for pc := range leaders {
		if pc >= 0 && pc < n {
			starts = append(starts, pc)
		} else if pc != n {
			return nil, fmt.Errorf("jump target %d out of range", pc)
		}
	}
	slices.Sort(starts)
	order := make([]*Block, 0, len(starts)+2)
	for _, pc := range starts {
		blk := &Block{PC: pc}
		b.blocks[pc] = blk
		order = append(order, blk)
	}
	at := func(pc int) (*Block, error) {
		if blk, ok := b.blocks[pc]; ok {
			return blk, nil
		}
		return nil, fmt.Errorf("no block at pc %d", pc)
	}

	for bi, blk := range order {
		end := n
		if bi+1 < len(order) {
			end = order[bi+1].PC
		}
		var next *Block
		if end < n {
			next = order[bi+1]
		}
		blk.Term = Term{Kind: TFall, Next: next}
		for pc := blk.PC; pc < end; pc++ {
			if err := b.translate(blk, pc, at, target); err != nil {
				return nil, fmt.Errorf("pc %d (%s): %w", pc, code[pc].Op, err)
			}
		}
		if blk.Term.Kind == TFall && next == nil {
			// Falling off the end can only happen in malformed code; treat as return.
			blk.Stmts = append(blk.Stmts, &Return{})
			blk.Term = Term{Kind: TReturn}
		}
	}
	// FORI jumps to just past its FORL, which ends a block.
	for _, blk := range order {
		if blk.Term.Kind != TForPrep {
			continue
		}
		exit := blk.Term.Next.PC
		for _, c := range order {
			if c.PC < exit && c.Term.Kind == TForLoop && c.Term.Then == blk.Term.Then {
				blk.Term.Latch = c
			}
		}
		if blk.Term.Latch == nil {
			return nil, fmt.Errorf("numeric for at pc %d has no FORL", blk.PC)
		}
	}

	// Synthetic entry block defining the parameters.
	entry := &Block{PC: -1, Term: Term{Kind: TFall, Next: order[0]}}
	params := &defStmt{}
	for i := range p.NumParams {
		params.Regs = append(params.Regs, b.reg(i))
	}
	entry.Stmts = []Stmt{params}
	order = append([]*Block{entry}, order...)
	for i, blk := range order {
		blk.ID = i
	}
	return order, nil
}

func (b *irBuilder) emit(blk *Block, s Stmt) { blk.Stmts = append(blk.Stmts, s) }

func (b *irBuilder) assign(blk *Block, dst int, src Expr) {
	b.emit(blk, &Assign{Dst: []Expr{b.reg(dst)}, Src: []Expr{src}})
}

func (b *irBuilder) translate(blk *Block, pc int, at func(int) (*Block, error), target func(int) int) error {
	ins := b.p.Code[pc]
	a, bb, c, d := int(ins.A), int(ins.B), int(ins.C), int(ins.D)
	argBase := a + 1 + b.fr2
	var err error
	jumpTo := func(pc int) *Block {
		var t *Block
		if err == nil {
			t, err = at(pc)
		}
		return t
	}
	cond := func(tested Expr, negated bool, copyReg int) {
		blk.Term = Term{Kind: TCond, Tested: tested, Negated: negated, Copy: copyReg,
			Then: jumpTo(pc + 1), Next: jumpTo(pc + 2)}
	}
	cmp := func(op string, r Expr) { cond(&Binop{Op: op, L: b.reg(a), R: r}, false, -1) }
	// Register comparisons: LuaJIT turns "x > y" into "y < x", so write the
	// operand that was evaluated first on the left (and constants on the right).
	cmpVV := func(op string) {
		if b.swapOperands(blk, pc, a, d) {
			cond(&Binop{Op: mirror[op], L: b.reg(d), R: b.reg(a)}, false, -1)
		} else {
			cmp(op, b.reg(d))
		}
	}

	switch ins.Op {
	case bytecode.ISLT:
		cmpVV("<")
	case bytecode.ISGE:
		cmpVV(">=")
	case bytecode.ISLE:
		cmpVV("<=")
	case bytecode.ISGT:
		cmpVV(">")
	case bytecode.ISEQV:
		cmpVV("==")
	case bytecode.ISNEV:
		cmpVV("~=")
	case bytecode.ISEQS:
		cmp("==", b.strKey(d))
	case bytecode.ISNES:
		cmp("~=", b.strKey(d))
	case bytecode.ISEQN:
		cmp("==", b.kn(d))
	case bytecode.ISNEN:
		cmp("~=", b.kn(d))
	case bytecode.ISEQP:
		cmp("==", pri(d))
	case bytecode.ISNEP:
		cmp("~=", pri(d))
	case bytecode.IST:
		cond(b.reg(d), false, -1)
	case bytecode.ISF:
		cond(b.reg(d), true, -1)
	case bytecode.ISTC:
		cond(b.reg(d), false, a)
	case bytecode.ISFC:
		cond(b.reg(d), true, a)

	case bytecode.MOV:
		b.assign(blk, a, b.reg(d))
	case bytecode.NOT:
		b.assign(blk, a, &Unop{Op: "not", X: b.reg(d)})
	case bytecode.UNM:
		b.assign(blk, a, &Unop{Op: "-", X: b.reg(d)})
	case bytecode.LEN:
		b.assign(blk, a, &Unop{Op: "#", X: b.reg(d)})

	case bytecode.ADDVN, bytecode.SUBVN, bytecode.MULVN, bytecode.DIVVN, bytecode.MODVN:
		b.assign(blk, a, &Binop{Op: arith(ins.Op), L: b.reg(bb), R: b.kn(c)})
	case bytecode.ADDNV, bytecode.SUBNV, bytecode.MULNV, bytecode.DIVNV, bytecode.MODNV:
		b.assign(blk, a, &Binop{Op: arith(ins.Op), L: b.kn(c), R: b.reg(bb)})
	case bytecode.ADDVV, bytecode.SUBVV, bytecode.MULVV, bytecode.DIVVV, bytecode.MODVV, bytecode.POW:
		b.assign(blk, a, &Binop{Op: arith(ins.Op), L: b.reg(bb), R: b.reg(c)})
	case bytecode.CAT:
		var e Expr = b.reg(c)
		for r := c - 1; r >= bb; r-- {
			e = &Binop{Op: "..", L: b.reg(r), R: e}
		}
		b.assign(blk, a, e)

	case bytecode.KSTR, bytecode.KHASH:
		b.assign(blk, a, b.strKey(d))
	case bytecode.KSHORT:
		b.assign(blk, a, intConst(int64(int16(uint16(d)))))
	case bytecode.KNUM:
		b.assign(blk, a, b.kn(d))
	case bytecode.KPRI:
		b.assign(blk, a, pri(d))
	case bytecode.KNIL:
		for r := a; r <= d; r++ {
			b.assign(blk, r, pri(0))
		}

	case bytecode.UGET:
		b.assign(blk, a, &Upval{Index: d, Fn: b.fn})
	case bytecode.USETV:
		b.emit(blk, &Assign{Dst: []Expr{&Upval{Index: a, Fn: b.fn}}, Src: []Expr{b.reg(d)}})
	case bytecode.USETS:
		b.emit(blk, &Assign{Dst: []Expr{&Upval{Index: a, Fn: b.fn}}, Src: []Expr{b.strKey(d)}})
	case bytecode.USETN:
		b.emit(blk, &Assign{Dst: []Expr{&Upval{Index: a, Fn: b.fn}}, Src: []Expr{b.kn(d)}})
	case bytecode.USETP:
		b.emit(blk, &Assign{Dst: []Expr{&Upval{Index: a, Fn: b.fn}}, Src: []Expr{pri(d)}})
	case bytecode.UCLO:
		b.emit(blk, &uclo{From: a})
		if d != 0x8000 {
			blk.Term = Term{Kind: TJump, Next: jumpTo(target(pc))}
		}
	case bytecode.FNEW:
		k := b.kgc(d)
		if k.Kind != bytecode.KProto {
			return fmt.Errorf("FNEW of non-function constant")
		}
		child := newFunction(k.Proto, b.fn.file)
		cl := &Closure{Fn: child}
		for _, uv := range k.Proto.Upvalues {
			if uv.Local {
				cl.Caps = append(cl.Caps, b.reg(uv.Index))
			} else {
				cl.Caps = append(cl.Caps, &Upval{Index: uv.Index, Fn: b.fn})
			}
		}
		b.assign(blk, a, cl)

	case bytecode.TNEW:
		b.assign(blk, a, &Table{hint: d})
	case bytecode.TDUP:
		k := b.kgc(d)
		if k.Kind != bytecode.KTable {
			return fmt.Errorf("TDUP of non-table constant")
		}
		b.assign(blk, a, templateTable(k.Table))
	case bytecode.GGET:
		b.assign(blk, a, &Global{Key: b.kgc(d)})
	case bytecode.GSET:
		b.emit(blk, &Assign{Dst: []Expr{&Global{Key: b.kgc(d)}}, Src: []Expr{b.reg(a)}})
	case bytecode.TGETV, bytecode.TGETR:
		b.getIndex(blk, pc, a, bb, b.reg(c))
	case bytecode.TGETS:
		b.getIndex(blk, pc, a, bb, b.strKey(c))
	case bytecode.TGETB:
		b.assign(blk, a, &Index{Obj: b.reg(bb), Key: intConst(int64(c))})
	case bytecode.TSETV, bytecode.TSETR:
		b.emit(blk, &Assign{Dst: []Expr{&Index{Obj: b.reg(bb), Key: b.reg(c)}}, Src: []Expr{b.reg(a)}})
	case bytecode.TSETS:
		b.emit(blk, &Assign{Dst: []Expr{&Index{Obj: b.reg(bb), Key: b.strKey(c)}}, Src: []Expr{b.reg(a)}})
	case bytecode.TSETB:
		b.emit(blk, &Assign{Dst: []Expr{&Index{Obj: b.reg(bb), Key: intConst(int64(c))}}, Src: []Expr{b.reg(a)}})
	case bytecode.TSETM:
		var start int64
		if d < len(b.p.KN) {
			k := b.p.KN[d]
			if k.Kind == bytecode.KNumber {
				start = int64(int32(uint32(math.Float64bits(k.Num))))
			} else {
				start = k.Int
			}
		}
		b.emit(blk, &tsetMulti{T: b.reg(a - 1), Start: start, Vals: b.reg(regMultRes)})

	case bytecode.CALL, bytecode.CALLM:
		args := b.regs(argBase, argBase+c-2)
		if ins.Op == bytecode.CALLM {
			args = append(b.regs(argBase, argBase+c-1), b.reg(regMultRes))
		}
		call := &Call{Fn: b.reg(a), Args: args}
		switch bb {
		case 0:
			call.Multi = true
			b.assign(blk, regMultRes, call)
		case 1:
			b.emit(blk, &CallStmt{Call: call})
		default:
			dst := b.regs(a, a+bb-2)
			if len(dst) > 1 {
				call.Multi = true
			}
			b.emit(blk, &Assign{Dst: dst, Src: []Expr{call}})
		}
	case bytecode.CALLT, bytecode.CALLMT:
		args := b.regs(argBase, argBase+d-2)
		if ins.Op == bytecode.CALLMT {
			args = append(b.regs(argBase, argBase+d-1), b.reg(regMultRes))
		}
		b.emit(blk, &Return{Vals: []Expr{&Call{Fn: b.reg(a), Args: args, Multi: true}}})
		blk.Term = Term{Kind: TReturn}
	case bytecode.VARG:
		if bb == 0 {
			b.assign(blk, regMultRes, &Vararg{Multi: true})
		} else {
			dst := b.regs(a, a+bb-2)
			b.emit(blk, &Assign{Dst: dst, Src: []Expr{&Vararg{Multi: len(dst) > 1}}})
		}

	case bytecode.RETM:
		b.emit(blk, &Return{Vals: append(b.regs(a, a+d-1), b.reg(regMultRes))})
		blk.Term = Term{Kind: TReturn}
	case bytecode.RET:
		b.emit(blk, &Return{Vals: b.regs(a, a+d-2)})
		blk.Term = Term{Kind: TReturn}
	case bytecode.RET0:
		b.emit(blk, &Return{})
		blk.Term = Term{Kind: TReturn}
	case bytecode.RET1:
		b.emit(blk, &Return{Vals: []Expr{b.reg(a)}})
		blk.Term = Term{Kind: TReturn}

	case bytecode.FORI:
		body := jumpTo(pc + 1)
		blk.Term = Term{Kind: TForPrep, Base: a, Args: b.regs(a, a+2), Then: body, Next: jumpTo(target(pc))}
		if err == nil {
			body.Stmts = append([]Stmt{&defStmt{Regs: []*Reg{b.reg(a + 3)}, Loop: true}}, body.Stmts...)
		}
	case bytecode.FORL:
		blk.Term = Term{Kind: TForLoop, Base: a, Then: jumpTo(target(pc)), Next: jumpTo(pc + 1)}
	case bytecode.JMP, bytecode.ISNEXT:
		t := target(pc)
		code := b.p.Code
		// A for-in setup jumps to ITERC/ITERN whose ITERL loops back to just
		// after this jump; other jumps there are "continue"s.
		if t+1 < len(code) && (code[t].Op == bytecode.ITERC || code[t].Op == bytecode.ITERN) &&
			code[t+1].Op == bytecode.ITERL && target(t+1) == pc+1 {
			base := int(code[t].A)
			body := jumpTo(pc + 1)
			blk.Term = Term{Kind: TIterJump, Base: base, NRes: int(code[t].B) - 1,
				Args: b.regs(base-3, base-1), Then: body, Next: jumpTo(t)}
			if err == nil {
				def := &defStmt{Loop: true}
				for r := base; r < base+int(code[t].B)-1; r++ {
					def.Regs = append(def.Regs, b.reg(r))
				}
				body.Stmts = append([]Stmt{def}, body.Stmts...)
			}
		} else {
			blk.Term = Term{Kind: TJump, Next: jumpTo(t)}
		}
	case bytecode.ITERC, bytecode.ITERN:
		// Handled together with the following ITERL.
	case bytecode.ITERL:
		blk.Term = Term{Kind: TIterLoop, Base: a, Then: jumpTo(target(pc)), Next: jumpTo(pc + 1)}
	case bytecode.LOOP:
		// A hint for the JIT; loops are recovered from back edges.
	default:
		return fmt.Errorf("unsupported opcode")
	}
	return err
}

var mirror = map[string]string{"<": ">", ">": "<", "<=": ">=", ">=": "<=", "==": "==", "~=": "~="}

// swapOperands reports whether comparison operands a and d were evaluated in
// the order d, a in the source, judging by where they were last written in
// this block. A constant load on the left is also moved to the right.
func (b *irBuilder) swapOperands(blk *Block, pc, a, d int) bool {
	defA, defD := -1, -1
	for i := pc - 1; i >= blk.PC && (defA < 0 || defD < 0); i-- {
		ins := b.p.Code[i]
		if ma, _, _ := ins.Op.Modes(); ma != bytecode.MDst {
			continue
		}
		switch int(ins.A) {
		case a:
			defA = max(defA, i)
		case d:
			defD = max(defD, i)
		}
	}
	if defA >= 0 && defD >= 0 {
		return defD < defA
	}
	return defA >= 0 && isConstLoad(b.p.Code[defA].Op) && (defD < 0 || !isConstLoad(b.p.Code[defD].Op))
}

func isConstLoad(op bytecode.Op) bool {
	switch op {
	case bytecode.KSTR, bytecode.KHASH, bytecode.KSHORT, bytecode.KNUM, bytecode.KPRI:
		return true
	}
	return false
}

func arith(op bytecode.Op) string {
	switch op {
	case bytecode.ADDVN, bytecode.ADDNV, bytecode.ADDVV:
		return "+"
	case bytecode.SUBVN, bytecode.SUBNV, bytecode.SUBVV:
		return "-"
	case bytecode.MULVN, bytecode.MULNV, bytecode.MULVV:
		return "*"
	case bytecode.DIVVN, bytecode.DIVNV, bytecode.DIVVV:
		return "/"
	case bytecode.MODVN, bytecode.MODNV, bytecode.MODVV:
		return "%"
	}
	return "^"
}

// getIndex emits a table read, recognizing the method-call prologue
// "MOV self, obj; TGETS func, obj, key" (optionally with the key loaded into a register).
func (b *irBuilder) getIndex(blk *Block, pc, a, obj int, key Expr) {
	code := b.p.Code
	self := a + 1 + b.fr2
	// Each of these instructions emitted exactly one statement, so they are the
	// block's trailing statements when they sit just before pc in the same block.
	isMov := func(at int) bool {
		return at >= blk.PC && code[at].Op == bytecode.MOV && int(code[at].A) == self && int(code[at].D) == obj
	}
	isKeyLoad := func(at, r int) bool {
		return at >= blk.PC && int(code[at].A) == r && (code[at].Op == bytecode.KSTR || code[at].Op == bytecode.KHASH)
	}
	n := len(blk.Stmts)
	kr, keyInReg := key.(*Reg)
	switch {
	case !keyInReg && isMov(pc-1):
		blk.Stmts = blk.Stmts[:n-1]
	case keyInReg && kr.N == self+1 && isKeyLoad(pc-1, kr.N) && isMov(pc-2):
		// Long-key form: MOV self, obj; KSTR key; TGETV func, obj, key.
		key = blk.Stmts[n-1].(*Assign).Src[0]
		blk.Stmts = blk.Stmts[:n-2]
	default:
		b.assign(blk, a, &Index{Obj: b.reg(obj), Key: key})
		return
	}
	b.emit(blk, &methodDef{Fn: b.reg(a), Self: b.reg(self), Obj: b.reg(obj), Key: key})
}

// templateTable converts a TDUP template into a constructor.
func templateTable(t *bytecode.Table) *Table {
	out := &Table{tmpl: t}
	for i, v := range t.Array {
		if i == 0 && v.Kind == bytecode.KNil {
			continue // slot 0 is always present in the array part
		}
		if v.Kind == bytecode.KNil {
			continue // placeholder filled by a later TSETB
		}
		out.Fields = append(out.Fields, Field{Key: intConst(int64(i)), Value: &Const{K: v}})
	}
	for _, e := range t.Hash {
		if e.Value.Kind == bytecode.KNil {
			continue // placeholder for a value set by a following TSETS/TSETV
		}
		out.Fields = append(out.Fields, Field{Key: &Const{K: e.Key}, Value: &Const{K: e.Value}})
	}
	return out
}
