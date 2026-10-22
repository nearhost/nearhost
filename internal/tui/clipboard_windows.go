package tui

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The clipboard calls are in user32 and kernel32, which x/sys does not wrap.
var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	emptyClipboard   = user32.NewProc("EmptyClipboard")
	setClipboardData = user32.NewProc("SetClipboardData")
	globalAlloc      = kernel32.NewProc("GlobalAlloc")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
	globalFree       = kernel32.NewProc("GlobalFree")
	moveMemory       = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13     // CF_UNICODETEXT
	gmemMoveable  = 0x0002 // GMEM_MOVEABLE
)

// systemClipboard puts text on the clipboard as Unicode text through the
// Win32 API. clip.exe would keep a byte order mark as a character, or read
// text without one in the console's code page. Tests replace it, so they
// leave the clipboard alone.
var systemClipboard = func(text string) error {
	units, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	// The clipboard is opened for the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openWithRetry(); err != nil {
		return err
	}
	defer closeClipboard.Call()
	if r, _, err := emptyClipboard.Call(); r == 0 {
		return fmt.Errorf("empty clipboard: %w", err)
	}
	size := uintptr(len(units)) * unsafe.Sizeof(units[0])
	h, _, err := globalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("allocate clipboard memory: %w", err)
	}
	p, _, err := globalLock.Call(h)
	if p == 0 {
		globalFree.Call(h)
		return fmt.Errorf("lock clipboard memory: %w", err)
	}
	// Copied by the system: p is memory Go does not own.
	moveMemory.Call(p, uintptr(unsafe.Pointer(&units[0])), size)
	globalUnlock.Call(h)
	// On success the clipboard owns the memory.
	if r, _, err := setClipboardData.Call(cfUnicodeText, h); r == 0 {
		globalFree.Call(h)
		return fmt.Errorf("set clipboard: %w", err)
	}
	return nil
}

// openWithRetry opens the clipboard, waiting briefly while another program
// has it open.
func openWithRetry() error {
	var err error
	for range 10 {
		var r uintptr
		if r, _, err = openClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.Join(errors.New("open clipboard"), err)
}
