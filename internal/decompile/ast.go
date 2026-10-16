// Package decompile turns parsed LuaJIT prototypes into Lua source.
package decompile

import "github.com/ifarbod/codljdec/internal/bytecode"

// Expr is a Lua expression. During analysis, register reads appear as *Reg;
// after variable assignment they are replaced by *VarRef.
type Expr interface{ isExpr() }

type (
	// Reg is a register: a read in operand position (Use is set by def-use
	// analysis) or a write in destination position (Def is set).
	Reg struct {
		N   int
		Use *use
		Def *def
	}
	// VarRef reads or writes a named local, parameter or upvalue.
	VarRef struct{ V *Variable }
	// Upval reads or writes an upvalue of the current function.
	Upval struct {
		Index int
		Fn    *Function
	}
	// Global reads or writes a global.
	Global struct{ Key bytecode.Const }
	// Index is Obj[Key] (printed as Obj.Key for identifier strings).
	Index struct{ Obj, Key Expr }
	// Const is a literal.
	Const struct{ K bytecode.Const }
	// Vararg is "...".
	Vararg struct{ Multi bool }
	// Call is a function or method call. For a method call, Fn is an *Index
	// whose Obj is the receiver, and Args excludes the receiver.
	Call struct {
		Fn     Expr
		Args   []Expr
		Method bool
		Multi  bool // used in a context that keeps all results
	}
	// Binop is a binary operation, including "and", "or" and "..".
	Binop struct {
		Op   string
		L, R Expr
	}
	// Unop is "not", "-" or "#".
	Unop struct {
		Op string
		X  Expr
	}
	// Closure is a function literal. Caps holds, per upvalue of Fn, the
	// captured register (or enclosing upvalue) at the creation site.
	Closure struct {
		Fn   *Function
		Caps []Expr
	}
	// Table is a table constructor. Fields keep evaluation order; the printer
	// turns runs of consecutive integer keys into positional items.
	Table struct {
		Fields     []Field
		Multi      Expr  // trailing open expression (call or vararg), if any
		MultiStart int64 // array index Multi's first value lands at
		// How the table was created, which tells which following writes
		// came from the constructor: TDUP's template, or TNEW's size hint.
		tmpl *bytecode.Table
		hint int
	}
	// MultRes stands for the open results of the preceding call or vararg.
	MultRes struct{}
)

// Field is a keyed table constructor entry.
type Field struct{ Key, Value Expr }

func (*Reg) isExpr()     {}
func (*VarRef) isExpr()  {}
func (*Upval) isExpr()   {}
func (*Global) isExpr()  {}
func (*Index) isExpr()   {}
func (*Const) isExpr()   {}
func (*Vararg) isExpr()  {}
func (*Call) isExpr()    {}
func (*Binop) isExpr()   {}
func (*Unop) isExpr()    {}
func (*Closure) isExpr() {}
func (*Table) isExpr()   {}
func (*MultRes) isExpr() {}

// Stmt is a Lua statement.
type Stmt interface{ isStmt() }

type (
	// Assign is "Dst... = Src..." (or a "local" declaration when Local is set).
	Assign struct {
		Dst   []Expr
		Src   []Expr
		Local bool
	}
	// CallStmt is a call evaluated for its side effects.
	CallStmt struct{ Call *Call }
	// Return returns values.
	Return struct{ Vals []Expr }
	// If is an if/elseif/else chain.
	If struct {
		Cond       Expr
		Then, Else []Stmt
	}
	// While is a while loop.
	While struct {
		Cond Expr
		Body []Stmt
	}
	// Repeat is a repeat-until loop.
	Repeat struct {
		Body []Stmt
		Cond Expr
	}
	// NumFor is a numeric for loop.
	NumFor struct {
		Var               Expr
		Start, Stop, Step Expr
		Body              []Stmt
	}
	// GenFor is a generic for loop.
	GenFor struct {
		Vars  []Expr
		Exprs []Expr
		Body  []Stmt
	}
	// Break exits the innermost loop.
	Break struct{}
	// Goto jumps to a label.
	Goto struct{ Label string }
	// Label is a goto target.
	Label struct{ Name string }
	// Comment is emitted verbatim (used for unsupported constructs).
	Comment struct{ Text string }
	// LocalDecl declares locals without assigning them.
	LocalDecl struct{ Vars []*Variable }
)

func (*Assign) isStmt()    {}
func (*CallStmt) isStmt()  {}
func (*Return) isStmt()    {}
func (*If) isStmt()        {}
func (*While) isStmt()     {}
func (*Repeat) isStmt()    {}
func (*NumFor) isStmt()    {}
func (*GenFor) isStmt()    {}
func (*Break) isStmt()     {}
func (*Goto) isStmt()      {}
func (*Label) isStmt()     {}
func (*Comment) isStmt()   {}
func (*LocalDecl) isStmt() {}

// Variable is a named value: a parameter, local, or loop variable.
type Variable struct {
	Name     string
	Param    bool
	LoopVar  bool // declared by a for loop
	Captured bool // referenced by a closure
	Reg      int  // the register holding it
	declared bool
}

// Function is a decompiled function.
type Function struct {
	Proto    *bytecode.Proto
	ID       int
	Params   []*Variable
	Vararg   bool
	Body     []Stmt
	Upvalues []*Variable // resolved from the parent's FNEW site
	Err      error       // decompilation failure; Body then holds a fallback
	file     *fileCtx
}

// fileCtx carries per-file state shared by all functions.
type fileCtx struct {
	fr2 bool
	ids map[*bytecode.Proto]int
}
