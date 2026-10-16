// Package bytecode parses the LuaJIT 2.1 bytecode dumps used by Call of Duty
// (IW8 / MW2019, S4 / Vanguard, IW9 / MWII, JUP / MWIII).
package bytecode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Variant identifies the on-disk dialect, taken from the header version byte.
type Variant uint8

const (
	VariantIW8 Variant = 0x02 // stock LuaJIT 2.1 format
	VariantS4  Variant = 0x82 // KHASH opcode, 64-bit hash constants
	VariantIW9 Variant = 0x83 // as S4, plus a hash-kind byte after each hash
)

func (v Variant) String() string {
	switch v {
	case VariantIW8:
		return "IW8"
	case VariantS4:
		return "S4"
	case VariantIW9:
		return "IW9"
	}
	return fmt.Sprintf("0x%02X", uint8(v))
}

// Header flag bits.
const (
	FlagBigEndian = 0x01
	FlagStrip     = 0x02
	FlagFFI       = 0x04
	FlagFR2       = 0x08
	FlagNoName    = 0x10 // CoD: chunk name omitted even when not stripped
)

// File is a parsed bytecode dump.
type File struct {
	Variant Variant
	Flags   uint32
	Name    string
	Main    *Proto
}

// FR2 reports whether calls use two-slot frames (function + frame link).
func (f *File) FR2() bool { return f.Flags&FlagFR2 != 0 }

// Proto is a function prototype.
type Proto struct {
	Flags     uint8
	NumParams int
	FrameSize int
	Upvalues  []Upvalue
	Code      []Ins   // excludes the implicit FUNCF/FUNCV header instruction
	KGC       []Const // GC constants, indexed as the bytecode does (0 = last on disk)
	KN        []Const // numeric constants
	Children  []*Proto
	FirstLine uint32
	LineCount uint32
	Lines     []uint32 // per-instruction line numbers, when present
}

// Proto flag bits.
const (
	ProtoChild  = 0x01
	ProtoVararg = 0x02
	ProtoFFI    = 0x04
	ProtoNoJIT  = 0x08
	ProtoILoop  = 0x10
)

// IsVararg reports whether the function takes "...".
func (p *Proto) IsVararg() bool { return p.Flags&ProtoVararg != 0 }

// Upvalue describes where a closure's upvalue comes from.
type Upvalue struct {
	Index     int  // slot in the parent's frame, or the parent's upvalue index
	Local     bool // captured from a parent register (else from a parent upvalue)
	Immutable bool
}

// Ins is a decoded instruction.
type Ins struct {
	Op      Op
	A, B, C uint8
	D       uint16
}

// SD returns D as a signed jump offset relative to the next instruction.
func (i Ins) SD() int { return int(i.D) - 0x8000 }

// ConstKind enumerates constant types.
type ConstKind uint8

const (
	KNil ConstKind = iota
	KFalse
	KTrue
	KInt
	KNumber
	KString
	KHash
	KTable
	KProto
	KCData
)

// Const is a constant value.
type Const struct {
	Kind     ConstKind
	Int      int64
	Num      float64
	Str      string
	Hash     uint64
	HashKind uint8 // IW9+: the byte stored after a hash (asset type)
	Table    *Table
	Proto    *Proto
}

// Table is a template table (TDUP).
type Table struct {
	Array []Const // index 0 is t[0]
	Hash  []TableEntry
}

// TableEntry is a key/value pair of a template table's hash part.
type TableEntry struct{ Key, Value Const }

type reader struct {
	buf []byte
	pos int
	err error
}

var errEOF = errors.New("unexpected end of data")

// ErrEmpty is returned for zero-length input (the dumps contain a few of these).
var ErrEmpty = errors.New("empty file")

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = fmt.Errorf("at offset 0x%X: %w", r.pos, err)
	}
}

func (r *reader) byte() uint8 {
	if r.pos >= len(r.buf) {
		r.fail(errEOF)
		return 0
	}
	b := r.buf[r.pos]
	r.pos++
	return b
}

func (r *reader) bytes(n int) []byte {
	if n < 0 || r.pos+n > len(r.buf) {
		r.fail(errEOF)
		r.pos = len(r.buf)
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *reader) uleb() uint64 {
	var v uint64
	for shift := 0; ; shift += 7 {
		b := r.byte()
		if shift < 64 {
			v |= uint64(b&0x7f) << shift
		}
		if b < 0x80 || r.err != nil {
			return v
		}
	}
}

// uleb33 reads the 33-bit ULEB128 used for numeric constants; the low bit of
// the first byte says whether the value is a double (true) or an int32.
func (r *reader) uleb33() (uint32, bool) {
	b := r.byte()
	isNum := b&1 != 0
	v := uint32(b >> 1)
	if v >= 0x40 {
		v &= 0x3f
		for shift := 6; ; shift += 7 {
			b = r.byte()
			v |= uint32(b&0x7f) << shift
			if b < 0x80 || r.err != nil {
				break
			}
		}
	}
	return v, isNum
}

// Parse decodes a bytecode dump.
func Parse(data []byte) (*File, error) {
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	r := &reader{buf: data}
	if magic := r.bytes(3); r.err != nil || string(magic) != "\x1bLJ" {
		return nil, errors.New("not a LuaJIT bytecode file")
	}
	f := &File{Variant: Variant(r.byte()), Flags: uint32(r.uleb())}
	switch f.Variant {
	case VariantIW8, VariantS4, VariantIW9:
	default:
		return nil, fmt.Errorf("unsupported bytecode version 0x%02X", uint8(f.Variant))
	}
	if f.Flags&FlagBigEndian != 0 {
		return nil, errors.New("big-endian bytecode is not supported")
	}
	if f.Flags&(FlagStrip|FlagNoName) == 0 {
		f.Name = string(r.bytes(int(r.uleb())))
	}
	ops := stockTable
	if f.Variant != VariantIW8 {
		ops = hashTable
	}

	var stack []*Proto
	for r.err == nil {
		size := int(r.uleb())
		if size == 0 {
			break
		}
		start := r.pos
		p := readProto(r, f, ops, &stack)
		if r.err == nil && r.pos-start != size {
			return nil, fmt.Errorf("prototype at 0x%X: read %d bytes, header says %d", start, r.pos-start, size)
		}
		stack = append(stack, p)
	}
	if r.err != nil {
		return nil, r.err
	}
	if len(stack) != 1 {
		return nil, fmt.Errorf("%d unreferenced prototypes at end of file", len(stack))
	}
	if r.pos != len(data) {
		return nil, fmt.Errorf("%d trailing bytes", len(data)-r.pos)
	}
	f.Main = stack[0]
	return f, nil
}

func readProto(r *reader, f *File, ops opTable, stack *[]*Proto) *Proto {
	p := &Proto{Flags: r.byte(), NumParams: int(r.byte()), FrameSize: int(r.byte())}
	numUV := int(r.byte())
	numKGC := int(r.uleb())
	numKN := int(r.uleb())
	numBC := int(r.uleb())
	var dbgSize int
	if f.Flags&FlagStrip == 0 {
		dbgSize = int(r.uleb())
		if dbgSize > 0 {
			p.FirstLine = uint32(r.uleb())
			p.LineCount = uint32(r.uleb())
		}
	}

	code := r.bytes(numBC * 4)
	p.Code = make([]Ins, numBC)
	for i := range numBC {
		w := binary.LittleEndian.Uint32(code[i*4:])
		if r.err != nil {
			break
		}
		op := int(w & 0xff)
		if op >= len(ops) {
			r.fail(fmt.Errorf("unknown opcode 0x%02X", op))
			return p
		}
		p.Code[i] = Ins{Op: ops[op], A: uint8(w >> 8), C: uint8(w >> 16), B: uint8(w >> 24), D: uint16(w >> 16)}
	}

	p.Upvalues = make([]Upvalue, numUV)
	for i := range numUV {
		v := binary.LittleEndian.Uint16(r.bytes(2))
		p.Upvalues[i] = Upvalue{Index: int(v & 0x3fff), Local: v&0x8000 != 0, Immutable: v&0x4000 != 0}
	}

	// GC constants are stored in reverse: the bytecode's index 0 is the last one on disk.
	p.KGC = make([]Const, numKGC)
	for i := range numKGC {
		p.KGC[numKGC-1-i] = readKGC(r, f.Variant, p, stack)
	}
	p.KN = make([]Const, numKN)
	for i := range numKN {
		lo, isNum := r.uleb33()
		if isNum {
			hi := uint32(r.uleb())
			p.KN[i] = Const{Kind: KNumber, Num: math.Float64frombits(uint64(hi)<<32 | uint64(lo))}
		} else {
			p.KN[i] = Const{Kind: KInt, Int: int64(int32(lo))}
		}
	}

	dbg := r.bytes(dbgSize)
	if p.LineCount > 0 && len(dbg) > 0 {
		p.Lines = readLines(dbg, p, numBC)
	}
	return p
}

func readLines(dbg []byte, p *Proto, n int) []uint32 {
	width := 4
	switch {
	case p.LineCount < 256:
		width = 1
	case p.LineCount < 65536:
		width = 2
	}
	if len(dbg) < n*width {
		return nil
	}
	lines := make([]uint32, n)
	for i := range n {
		var d uint32
		switch width {
		case 1:
			d = uint32(dbg[i])
		case 2:
			d = uint32(binary.LittleEndian.Uint16(dbg[i*2:]))
		default:
			d = binary.LittleEndian.Uint32(dbg[i*4:])
		}
		lines[i] = p.FirstLine + d
	}
	return lines
}

// String constants start at this type tag; below it are child/table/hash kinds.
func strBase(v Variant) uint64 {
	if v == VariantIW8 {
		return 5
	}
	return 6
}

func readKGC(r *reader, v Variant, p *Proto, stack *[]*Proto) Const {
	t := r.uleb()
	switch {
	case t >= strBase(v):
		return Const{Kind: KString, Str: string(r.bytes(int(t - strBase(v))))}
	case t == 0: // child prototype, popped from the stack of already-read protos
		s := *stack
		if len(s) == 0 {
			r.fail(errors.New("child prototype reference with empty stack"))
			return Const{}
		}
		child := s[len(s)-1]
		*stack = s[:len(s)-1]
		p.Children = append(p.Children, child)
		return Const{Kind: KProto, Proto: child}
	case t == 1:
		return Const{Kind: KTable, Table: readTable(r, v)}
	case t == 4 && v != VariantIW8:
		return readHash(r, v)
	}
	r.fail(fmt.Errorf("unsupported GC constant type %d", t))
	return Const{}
}

func readHash(r *reader, v Variant) Const {
	hi := r.uleb()
	lo := r.uleb()
	c := Const{Kind: KHash, Hash: hi<<32 | lo}
	if v == VariantIW9 {
		c.HashKind = r.byte()
	}
	return c
}

func readTable(r *reader, v Variant) *Table {
	t := &Table{}
	na, nh := int(r.uleb()), int(r.uleb())
	t.Array = make([]Const, na)
	for i := range na {
		t.Array[i] = readTabK(r, v)
	}
	t.Hash = make([]TableEntry, nh)
	for i := range nh {
		k := readTabK(r, v)
		t.Hash[i] = TableEntry{Key: k, Value: readTabK(r, v)}
	}
	return t
}

func readTabK(r *reader, v Variant) Const {
	t := r.uleb()
	switch {
	case t >= strBase(v):
		return Const{Kind: KString, Str: string(r.bytes(int(t - strBase(v))))}
	case t == 5 && v != VariantIW8:
		return readHash(r, v)
	case t == 4:
		lo, hi := uint32(r.uleb()), uint32(r.uleb())
		return Const{Kind: KNumber, Num: math.Float64frombits(uint64(hi)<<32 | uint64(lo))}
	case t == 3:
		return Const{Kind: KInt, Int: int64(int32(uint32(r.uleb())))}
	case t == 2:
		return Const{Kind: KTrue}
	case t == 1:
		return Const{Kind: KFalse}
	}
	return Const{Kind: KNil}
}
