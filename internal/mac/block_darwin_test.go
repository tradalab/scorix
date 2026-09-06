//go:build darwin

package mac

import (
	"testing"

	"github.com/ebitengine/purego"
)

// The block literal is the riskiest thing in this package: a wrong field offset
// is a crash rather than an error, and no build or vet can see it. Calling the
// invoke pointer the way Objective-C would is the only cheap proof.
func TestBlockInvokeCarriesItsArgument(t *testing.T) {
	mustInit(t)
	var ran bool
	var gotSelf, gotArg uintptr
	block, err := NewBlock(func(self uintptr, arg uintptr) uintptr {
		ran, gotSelf, gotArg = true, self, arg
		return 0
	})
	if err != nil {
		t.Fatalf("NewBlock: %v", err)
	}
	InvokeBlockUint(block, 0x2a)
	if !ran {
		t.Fatal("invoke never reached the Go function: the offset in the literal is wrong")
	}
	if gotSelf != block {
		t.Errorf("first argument = %#x, want the block itself %#x", gotSelf, block)
	}
	if gotArg != 0x2a {
		t.Fatalf("argument = %#x, want 0x2a", gotArg)
	}
}

func TestVoidBlockInvoke(t *testing.T) {
	mustInit(t)
	ran := false
	block, err := NewBlock(func(uintptr) uintptr { ran = true; return 0 })
	if err != nil {
		t.Fatalf("NewBlock: %v", err)
	}
	InvokeVoidBlock(block)
	if !ran {
		t.Fatal("a void block did not run")
	}
	InvokeVoidBlock(0) // a nil handler is normal and must be ignored
}

// _Block_copy hands a global block straight back. Anything else means the
// runtime did not believe the isa or the flags and took the heap path, which is
// the failure that would later show up as a crash inside AppKit.
func TestGlobalBlockSurvivesBlockCopy(t *testing.T) {
	mustInit(t)
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		t.Fatalf("dlopen libSystem: %v", err)
	}
	var blockCopy func(uintptr) uintptr
	purego.RegisterLibFunc(&blockCopy, lib, "_Block_copy")

	block, err := NewBlock(func(uintptr) uintptr { return 0 })
	if err != nil {
		t.Fatalf("NewBlock: %v", err)
	}
	if copied := blockCopy(block); copied != block {
		t.Fatalf("_Block_copy moved the block (%#x -> %#x): it is not seen as global", block, copied)
	}
}
