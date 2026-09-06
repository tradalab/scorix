//go:build darwin

package mac

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// A block is a C struct with a function pointer at a fixed offset. The layout is
// libclosure's Block_literal_1; Go lays these fields out identically on 64-bit
// (8 + 4 + 4 + 8 + 8), so the struct can be handed to Objective-C as is.
type blockLiteral struct {
	isa        uintptr
	flags      int32
	reserved   int32
	invoke     uintptr
	descriptor *blockDescriptor
}

type blockDescriptor struct {
	reserved uintptr
	size     uintptr
}

// BLOCK_IS_GLOBAL. A global block captures nothing, so the runtime never copies
// or disposes it and no copy/dispose helpers belong in the descriptor.
const blockIsGlobal int32 = 1 << 28

var (
	blockOnce  sync.Once
	globalIsa  uintptr
	blockErr   error
	blockMu    sync.Mutex
	liveBlocks []*blockLiteral // see below: these must outlive the call
)

func initBlocks() error {
	blockOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			blockErr = err
			return
		}
		globalIsa, blockErr = purego.Dlsym(lib, "_NSConcreteGlobalBlock")
	})
	return blockErr
}

// NewBlock wraps fn as an Objective-C block and returns its address. fn must
// take the block pointer as its first argument, then the block's own arguments.
//
// The returned block is never freed: a completion handler outlives the call that
// received it, so nothing here can know when the last reference dies. Every
// caller creates its block once per process - do not call this in a loop.
func NewBlock(fn interface{}) (uintptr, error) {
	if err := initBlocks(); err != nil {
		return 0, err
	}
	b := &blockLiteral{
		isa:    globalIsa,
		flags:  blockIsGlobal,
		invoke: purego.NewCallback(fn),
	}
	b.descriptor = &blockDescriptor{size: unsafe.Sizeof(blockLiteral{})}
	// Pinned in a package var, not just kept on the stack: Objective-C holds the
	// pointer past this return, and Go's collector must not reclaim it.
	blockMu.Lock()
	liveBlocks = append(liveBlocks, b)
	blockMu.Unlock()
	return uintptr(unsafe.Pointer(b)), nil
}

// InvokeBlockUint is the shape of the presentation-options handler AppKit hands
// to a notification delegate.
func InvokeBlockUint(block uintptr, arg uint64) {
	if block == 0 {
		return
	}
	invoke := *(*uintptr)(unsafe.Pointer(block + 16))
	purego.SyscallN(invoke, block, uintptr(arg))
}

// InvokeVoidBlock is the shape of a delegate completion handler.
func InvokeVoidBlock(block uintptr) {
	if block == 0 {
		return
	}
	// invoke sits at offset 16: isa 0, flags 8, reserved 12.
	invoke := *(*uintptr)(unsafe.Pointer(block + 16))
	purego.SyscallN(invoke, block)
}
