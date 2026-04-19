#!/usr/bin/env bash
# Build balrog as a Windows GUI executable so double-clicking it doesn't
# pop a console window. The "-H=windowsgui" linker flag selects the GUI
# subsystem; without it Go defaults to the console subsystem.
set -e
go build -ldflags "-H=windowsgui" -o balrog.exe .
