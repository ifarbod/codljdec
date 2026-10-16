// Command codljdec decompiles Call of Duty LuaJIT bytecode dumps.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ifarbod/codljdec/internal/bytecode"
	"github.com/ifarbod/codljdec/internal/decompile"
)

func main() {
	disasm := flag.Bool("disasm", false, "print a bytecode listing instead of decompiling")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: codljdec [flags] <file>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	f, err := bytecode.Parse(data)
	if err != nil {
		fatal(err)
	}
	if *disasm {
		bytecode.Disassemble(os.Stdout, f)
		return
	}
	fn, err := decompile.File(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "codljdec: warning:", err)
	}
	if err := decompile.Print(os.Stdout, fn); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "codljdec:", err)
	os.Exit(1)
}
