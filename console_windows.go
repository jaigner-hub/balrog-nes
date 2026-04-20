//go:build windows

package main

// Belt-and-suspenders against the "a console window pops up when you
// double-click balrog.exe" regression. The proper fix is to build with
//
//     go build -ldflags "-H=windowsgui" -o balrog.exe .
//
// (see build.sh), which selects the GUI subsystem so Windows never
// opens a console for us in the first place. But `go build` alone
// defaults to the console subsystem and that's easy to forget.
//
// When a console-subsystem exe is launched from Explorer, Windows
// allocates a fresh console and attaches it to us (and only us). When
// it's launched from a terminal, it inherits the terminal's existing
// console (which has many attached processes). We detect the first
// case via GetConsoleProcessList: if we're the sole process on the
// console it's ours alone, and we FreeConsole to close the window.
// Terminal users keep their stdout — we don't touch a console we
// didn't cause to open.
//
// For a GUI-subsystem build there's no console at all and
// GetConsoleProcessList returns 0; this init is a harmless no-op.

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
)

func init() {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)),
	)
	// n == 0 → no console attached (GUI subsystem). n == 1 → fresh
	// console created for us alone (double-click from Explorer).
	// n >= 2 → inherited a shell's console; leave it alone so users
	// running from a terminal still see output.
	if n == 1 {
		procFreeConsole.Call()
	}
}
