package bytecode

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// HashMask keeps the 60 significant bits of a CoD hash (the top nibble is a type tag).
const HashMask = 0x0FFFFFFFFFFFFFFF

// FormatNumber renders a Lua number the way Lua source would spell it.
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "(0/0)"
	case math.IsInf(f, 1):
		return "math.huge"
	case math.IsInf(f, -1):
		return "-math.huge"
	case f == math.Trunc(f) && math.Abs(f) < 1e15:
		if f == 0 && math.Signbit(f) {
			return "-0"
		}
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// FormatHash renders a hash constant as a hex literal.
func FormatHash(h uint64) string { return fmt.Sprintf("0x%X", h&HashMask) }

// Quote renders a Lua string literal.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := range len(s) {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\v':
			b.WriteString(`\v`)
		default:
			if c < 0x20 || c == 0x7f {
				// Three digits so a following digit is never absorbed into the escape.
				fmt.Fprintf(&b, `\%03d`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// String renders a constant as a Lua literal (tables and protos are summarized).
func (c Const) String() string {
	switch c.Kind {
	case KNil:
		return "nil"
	case KFalse:
		return "false"
	case KTrue:
		return "true"
	case KInt:
		return strconv.FormatInt(c.Int, 10)
	case KNumber:
		return FormatNumber(c.Num)
	case KString:
		return Quote(c.Str)
	case KHash:
		return FormatHash(c.Hash)
	case KTable:
		var parts []string
		for i, v := range c.Table.Array {
			if i > 0 || v.Kind != KNil {
				parts = append(parts, fmt.Sprintf("[%d]=%s", i, v))
			}
		}
		for _, e := range c.Table.Hash {
			parts = append(parts, fmt.Sprintf("[%s]=%s", e.Key, e.Value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case KProto:
		return "<function>"
	}
	return "<cdata>"
}

// Number assigns function ids in depth-first order, visiting closures in the
// order their FNEW instructions appear. The decompiler uses the same ids.
func Number(main *Proto) map[*Proto]int {
	ids := map[*Proto]int{}
	var walk func(p *Proto)
	walk = func(p *Proto) {
		ids[p] = len(ids)
		for _, ins := range p.Code {
			if ins.Op == FNEW && int(ins.D) < len(p.KGC) {
				if c := p.KGC[ins.D].Proto; c != nil {
					if _, seen := ids[c]; !seen {
						walk(c)
					}
				}
			}
		}
	}
	walk(main)
	return ids
}

// Disassemble writes a listing of the whole file.
func Disassemble(w io.Writer, f *File) {
	fmt.Fprintf(w, "-- variant %s, flags 0x%X\n", f.Variant, f.Flags)
	ids := Number(f.Main)
	var walk func(p *Proto)
	walk = func(p *Proto) {
		disasmProto(w, p, fmt.Sprintf("f%d", ids[p]))
		for _, ins := range p.Code {
			if ins.Op == FNEW && int(ins.D) < len(p.KGC) {
				if c := p.KGC[ins.D].Proto; c != nil {
					walk(c)
				}
			}
		}
	}
	walk(f.Main)
}

// DisassembleProto writes a listing of a single prototype (not its children).
func DisassembleProto(w io.Writer, p *Proto, name string) { disasmProto(w, p, name) }

func disasmProto(w io.Writer, p *Proto, name string) {
	fmt.Fprintf(w, "\n-- function %s: params %d, frame %d, flags 0x%02X, upvalues %d, kgc %d, kn %d\n",
		name, p.NumParams, p.FrameSize, p.Flags, len(p.Upvalues), len(p.KGC), len(p.KN))
	for i, uv := range p.Upvalues {
		kind := "uv"
		if uv.Local {
			kind = "slot"
		}
		fmt.Fprintf(w, "--   upvalue %d = parent %s %d\n", i, kind, uv.Index)
	}
	for pc, ins := range p.Code {
		fmt.Fprintf(w, "%04d  %-7s %s\n", pc, ins.Op, operands(p, pc, ins))
	}
}

func operands(p *Proto, pc int, ins Ins) string {
	ma, mb, md := ins.Op.Modes()
	var parts []string
	add := func(m Mode, v int) {
		if m != MNone {
			parts = append(parts, operand(p, pc, m, v))
		}
	}
	add(ma, int(ins.A))
	if mb != MNone {
		add(mb, int(ins.B))
		add(md, int(ins.C))
	} else {
		add(md, int(ins.D))
	}
	return strings.Join(parts, " ")
}

func operand(p *Proto, pc int, m Mode, v int) string {
	switch m {
	case MDst, MBase, MRBase, MVar:
		return fmt.Sprintf("r%d", v)
	case MUV:
		return fmt.Sprintf("uv%d", v)
	case MLits:
		return strconv.Itoa(int(int16(uint16(v))))
	case MPri:
		return [...]string{"nil", "false", "true"}[min(v, 2)]
	case MNum:
		if v < len(p.KN) {
			return p.KN[v].String()
		}
	case MStr, MTab, MFunc, MCdata:
		if v < len(p.KGC) {
			return p.KGC[v].String()
		}
	case MJump:
		return fmt.Sprintf("=> %04d", pc+1+v-0x8000)
	}
	return strconv.Itoa(v)
}
