#!/usr/bin/env bash
# Canonical build for release. `-ldflags "-H=windowsgui"` selects the
# Windows GUI subsystem so double-clicking balrog.exe from Explorer
# doesn't pop a console window.
#
# A plain `go build` works for development but defaults to the Console
# subsystem. console_windows.go has a FreeConsole() fallback that
# closes the fresh console Windows opens in that case — but the
# console still flashes for a fraction of a second before the Go
# runtime gets there. Always release with this script.
set -e
go build -ldflags "-H=windowsgui" -o balrog.exe .
