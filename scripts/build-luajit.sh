#!/usr/bin/env sh
# Builds a stock LuaJIT 2.1 (x64, GC64) into .tools/luajit for the round-trip
# test, using "zig cc" as the C toolchain (no make or MSVC needed).
#
#   sh scripts/build-luajit.sh
#   LUAJIT=$PWD/.tools/luajit/src/luajit.exe go test ./internal/decompile -run RoundTrip
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$root/.tools/luajit"
if [ ! -d "$dir" ]; then
	git clone --depth 1 -b v2.1 https://github.com/LuaJIT/LuaJIT.git "$dir"
fi
cd "$dir/src"

CC="${CC:-zig cc -target x86_64-windows-gnu}"
EXE="${EXE:-.exe}"
ALL_LIB="lib_base.c lib_math.c lib_bit.c lib_string.c lib_table.c lib_io.c lib_os.c lib_package.c lib_debug.c lib_jit.c lib_ffi.c lib_buffer.c"

$CC -O2 -o "minilua$EXE" host/minilua.c
git -C "$dir" show -s --format=%ct > luajit_relver.txt
"./minilua$EXE" host/genversion.lua
"./minilua$EXE" ../dynasm/dynasm.lua -LN -D WIN -D JIT -D FFI -D ENDIAN_LE -D FPU -D P64 -o host/buildvm_arch.h vm_x64.dasc
$CC -O2 -I. -I../dynasm -o "buildvm$EXE" host/buildvm*.c
"./buildvm$EXE" -m peobj -o lj_vm.obj
for m in bcdef ffdef libdef recdef; do
	"./buildvm$EXE" -m $m -o lj_$m.h $ALL_LIB
done
"./buildvm$EXE" -m vmdef -o jit/vmdef.lua $ALL_LIB
"./buildvm$EXE" -m folddef -o lj_folddef.h lj_opt_fold.c
$CC -O2 -o "luajit$EXE" luajit.c ljamalg.c lj_vm.obj
"./luajit$EXE" -v
