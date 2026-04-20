@echo off
rem Canonical build for release. -H=windowsgui selects the Windows GUI
rem subsystem so double-clicking balrog.exe from Explorer doesn't pop
rem a console window. See build.sh for the bash equivalent.

go build -ldflags "-H=windowsgui" -o balrog.exe .
