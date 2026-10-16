package bytecode

// Op is a canonical LuaJIT 2.1 opcode. The numeric values of Op are internal;
// each file variant maps its own on-disk opcode bytes onto these.
type Op uint8

const (
	ISLT Op = iota
	ISGE
	ISLE
	ISGT
	ISEQV
	ISNEV
	ISEQS
	ISNES
	ISEQN
	ISNEN
	ISEQP
	ISNEP
	ISTC
	ISFC
	IST
	ISF
	ISTYPE
	ISNUM
	MOV
	NOT
	UNM
	LEN
	ADDVN
	SUBVN
	MULVN
	DIVVN
	MODVN
	ADDNV
	SUBNV
	MULNV
	DIVNV
	MODNV
	ADDVV
	SUBVV
	MULVV
	DIVVV
	MODVV
	POW
	CAT
	KSTR
	KHASH // CoD extension: load a 64-bit hash constant
	KCDATA
	KSHORT
	KNUM
	KPRI
	KNIL
	UGET
	USETV
	USETS
	USETN
	USETP
	UCLO
	FNEW
	TNEW
	TDUP
	GGET
	GSET
	TGETV
	TGETS
	TGETB
	TGETR
	TSETV
	TSETS
	TSETB
	TSETM
	TSETR
	CALLM
	CALL
	CALLMT
	CALLT
	ITERC
	ITERN
	VARG
	ISNEXT
	RETM
	RET
	RET0
	RET1
	FORI
	JFORI
	FORL
	IFORL
	JFORL
	ITERL
	IITERL
	JITERL
	LOOP
	ILOOP
	JLOOP
	JMP
	FUNCF
	IFUNCF
	JFUNCF
	FUNCV
	IFUNCV
	JFUNCV
	FUNCC
	FUNCCW
	numOps
)

// Operand kinds, mirroring LuaJIT's BCM* modes.
type Mode uint8

const (
	MNone  Mode = iota
	MDst        // destination slot
	MBase       // base slot (read-write)
	MRBase      // base slot (read-only)
	MVar        // variable slot
	MUV         // upvalue index
	MLit        // unsigned literal
	MLits       // signed literal
	MPri        // primitive: 0 nil, 1 false, 2 true
	MNum        // index into numeric constants
	MStr        // index into GC constants (string or hash)
	MTab        // index into GC constants (template table)
	MFunc       // index into GC constants (prototype)
	MCdata      // index into GC constants (cdata)
	MJump       // branch offset, biased by 0x8000
)

type opInfo struct {
	name    string
	a, b, d Mode // b == MNone means the instruction uses the AD format
}

var opInfos = [numOps]opInfo{
	ISLT: {"ISLT", MVar, MNone, MVar}, ISGE: {"ISGE", MVar, MNone, MVar},
	ISLE: {"ISLE", MVar, MNone, MVar}, ISGT: {"ISGT", MVar, MNone, MVar},
	ISEQV: {"ISEQV", MVar, MNone, MVar}, ISNEV: {"ISNEV", MVar, MNone, MVar},
	ISEQS: {"ISEQS", MVar, MNone, MStr}, ISNES: {"ISNES", MVar, MNone, MStr},
	ISEQN: {"ISEQN", MVar, MNone, MNum}, ISNEN: {"ISNEN", MVar, MNone, MNum},
	ISEQP: {"ISEQP", MVar, MNone, MPri}, ISNEP: {"ISNEP", MVar, MNone, MPri},
	ISTC: {"ISTC", MDst, MNone, MVar}, ISFC: {"ISFC", MDst, MNone, MVar},
	IST: {"IST", MNone, MNone, MVar}, ISF: {"ISF", MNone, MNone, MVar},
	ISTYPE: {"ISTYPE", MVar, MNone, MLit}, ISNUM: {"ISNUM", MVar, MNone, MLit},
	MOV: {"MOV", MDst, MNone, MVar}, NOT: {"NOT", MDst, MNone, MVar},
	UNM: {"UNM", MDst, MNone, MVar}, LEN: {"LEN", MDst, MNone, MVar},
	ADDVN: {"ADDVN", MDst, MVar, MNum}, SUBVN: {"SUBVN", MDst, MVar, MNum},
	MULVN: {"MULVN", MDst, MVar, MNum}, DIVVN: {"DIVVN", MDst, MVar, MNum},
	MODVN: {"MODVN", MDst, MVar, MNum}, ADDNV: {"ADDNV", MDst, MVar, MNum},
	SUBNV: {"SUBNV", MDst, MVar, MNum}, MULNV: {"MULNV", MDst, MVar, MNum},
	DIVNV: {"DIVNV", MDst, MVar, MNum}, MODNV: {"MODNV", MDst, MVar, MNum},
	ADDVV: {"ADDVV", MDst, MVar, MVar}, SUBVV: {"SUBVV", MDst, MVar, MVar},
	MULVV: {"MULVV", MDst, MVar, MVar}, DIVVV: {"DIVVV", MDst, MVar, MVar},
	MODVV: {"MODVV", MDst, MVar, MVar}, POW: {"POW", MDst, MVar, MVar},
	CAT:  {"CAT", MDst, MRBase, MRBase},
	KSTR: {"KSTR", MDst, MNone, MStr}, KHASH: {"KHASH", MDst, MNone, MStr},
	KCDATA: {"KCDATA", MDst, MNone, MCdata}, KSHORT: {"KSHORT", MDst, MNone, MLits},
	KNUM: {"KNUM", MDst, MNone, MNum}, KPRI: {"KPRI", MDst, MNone, MPri},
	KNIL: {"KNIL", MBase, MNone, MBase},
	UGET: {"UGET", MDst, MNone, MUV}, USETV: {"USETV", MUV, MNone, MVar},
	USETS: {"USETS", MUV, MNone, MStr}, USETN: {"USETN", MUV, MNone, MNum},
	USETP: {"USETP", MUV, MNone, MPri}, UCLO: {"UCLO", MRBase, MNone, MJump},
	FNEW: {"FNEW", MDst, MNone, MFunc},
	TNEW: {"TNEW", MDst, MNone, MLit}, TDUP: {"TDUP", MDst, MNone, MTab},
	GGET: {"GGET", MDst, MNone, MStr}, GSET: {"GSET", MVar, MNone, MStr},
	TGETV: {"TGETV", MDst, MVar, MVar}, TGETS: {"TGETS", MDst, MVar, MStr},
	TGETB: {"TGETB", MDst, MVar, MLit}, TGETR: {"TGETR", MDst, MVar, MVar},
	TSETV: {"TSETV", MVar, MVar, MVar}, TSETS: {"TSETS", MVar, MVar, MStr},
	TSETB: {"TSETB", MVar, MVar, MLit}, TSETM: {"TSETM", MBase, MNone, MNum},
	TSETR: {"TSETR", MVar, MVar, MVar},
	CALLM: {"CALLM", MBase, MLit, MLit}, CALL: {"CALL", MBase, MLit, MLit},
	CALLMT: {"CALLMT", MBase, MNone, MLit}, CALLT: {"CALLT", MBase, MNone, MLit},
	ITERC: {"ITERC", MBase, MLit, MLit}, ITERN: {"ITERN", MBase, MLit, MLit},
	VARG: {"VARG", MBase, MLit, MLit}, ISNEXT: {"ISNEXT", MBase, MNone, MJump},
	RETM: {"RETM", MBase, MNone, MLit}, RET: {"RET", MRBase, MNone, MLit},
	RET0: {"RET0", MRBase, MNone, MLit}, RET1: {"RET1", MRBase, MNone, MLit},
	FORI: {"FORI", MBase, MNone, MJump}, JFORI: {"JFORI", MBase, MNone, MJump},
	FORL: {"FORL", MBase, MNone, MJump}, IFORL: {"IFORL", MBase, MNone, MJump},
	JFORL: {"JFORL", MBase, MNone, MLit}, ITERL: {"ITERL", MBase, MNone, MJump},
	IITERL: {"IITERL", MBase, MNone, MJump}, JITERL: {"JITERL", MBase, MNone, MLit},
	LOOP: {"LOOP", MRBase, MNone, MJump}, ILOOP: {"ILOOP", MRBase, MNone, MJump},
	JLOOP: {"JLOOP", MRBase, MNone, MLit}, JMP: {"JMP", MRBase, MNone, MJump},
	FUNCF: {"FUNCF", MRBase, MNone, MNone}, IFUNCF: {"IFUNCF", MRBase, MNone, MNone},
	JFUNCF: {"JFUNCF", MRBase, MNone, MLit}, FUNCV: {"FUNCV", MRBase, MNone, MNone},
	IFUNCV: {"IFUNCV", MRBase, MNone, MNone}, JFUNCV: {"JFUNCV", MRBase, MNone, MLit},
	FUNCC: {"FUNCC", MRBase, MNone, MNone}, FUNCCW: {"FUNCCW", MRBase, MNone, MNone},
}

func (o Op) String() string {
	if o < numOps {
		return opInfos[o].name
	}
	return "UNKNOWN"
}

// Modes returns the operand modes of the opcode.
func (o Op) Modes() (a, b, d Mode) {
	i := opInfos[o]
	return i.a, i.b, i.d
}

// IsJump reports whether the D operand is a branch offset.
func (o Op) IsJump() bool { return opInfos[o].d == MJump }

// opTable maps on-disk opcode bytes to canonical opcodes for a file variant.
type opTable []Op

// stockTable is LuaJIT 2.1's own numbering (used by IW8 / MW2019).
var stockTable = func() opTable {
	t := make(opTable, 0, numOps)
	for o := range numOps {
		if o != KHASH {
			t = append(t, o)
		}
	}
	return t
}()

// hashTable is the CoD numbering with KHASH inserted after KSTR (S4, IW9, JUP).
var hashTable = func() opTable {
	t := make(opTable, 0, numOps)
	for o := range numOps {
		t = append(t, o)
	}
	return t
}()
