# codljdec

codljdec decompiles the LuaJIT 2.1 bytecode used by the UI scripts of Call of Duty: Modern Warfare (2019), Vanguard, Modern Warfare II and Modern Warfare III into Lua source.

```sh
go build ./cmd/codljdec
codljdec path/to/script.lua > script.dec.lua
codljdec -disasm path/to/script.lua
```

The input is a bytecode file that was already dumped from the game. codljdec does not read game archives or running processes.

## Supported formats

The header version byte selects the dialect.

| Version | Games | Differences from stock LuaJIT 2.1 |
|---|---|---|
| `0x02` | MW2019 (IW8) | none, the files are stock GC64 bytecode |
| `0x82` | Vanguard (S4) | a `KHASH` opcode after `KSTR` shifts the opcode numbers, 64-bit hash constants |
| `0x83` | MWII (IW9), MWIII (JUP) | as `0x82`, plus one type byte after each hash |

Hash constants print as hex literals masked to 60 bits, for example `Engine[0xED84C33EC5F01EA](...)`.

## How it works

1. `internal/bytecode` parses prototypes, constants and debug line info, and checks that each prototype consumes exactly its declared length.
2. `internal/decompile` builds basic blocks and links each register read to its reaching definitions.
3. Single-use temporaries are inlined only in reverse evaluation order, so calls keep their order. Method prologues become `obj:m(...)`. Table writes are folded into a constructor only when the `TDUP` template or the `TNEW` size hint shows the constructor produced them.
4. Branch regions whose exits all store the same register become `and`/`or` expressions, when the rewrite is exact. Chained tests become compound conditions.
5. Loops come from `FORI`/`FORL`, `ITERC`/`ITERL` and back edges. `if`/`else` comes from LuaJIT's code layout. A `goto` is emitted where no structured form fits.
6. Def-use webs become variables. Each is declared at the innermost scope that holds all its references. A function that fails to decompile prints as commented disassembly, and the rest of the file still decompiles.

## Testing

The tests read the bytecode corpus from `../Dump` relative to the repository root and skip when it is absent.

```sh
go test ./...
```

`TestRoundTrip` recompiles every decompiled file with a stock LuaJIT. All outputs must compile. MW2019 files use the stock format, so for them the recompiled bytecode is also compared with the original, instruction by instruction. Build LuaJIT with Zig as the C compiler and point the test at it:

```sh
sh scripts/build-luajit.sh
LUAJIT=$PWD/.tools/luajit/src/luajit.exe go test ./internal/decompile -run RoundTrip -v
```

## Known limitations

- Local names are generated (`f3_arg0`, `f3_local2`) because the dumps carry no variable names.
- Some functions with unstructured control flow produce a `goto` that LuaJIT rejects, either into a nested block or into the scope of a local.
- Keys of a printed template table appear in hash-part order, which can differ from the source.
- The CLI decompiles one file per run.

## Credits

The format details come from [CoDLuaDecompiler](https://github.com/zhm86/CoDLuaDecompiler) by zhm86. The test corpus is the CoDs_Lua_Dump_IW8Plus dump by HiNAtyu, with dumper improvements by BodNJenie.
