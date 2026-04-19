# balrog

A small NES emulator written in Go using [Ebitengine](https://ebitengine.org/) for the window, input, and audio.

Built end-to-end in a single live session as a learning project. Runs *Super Mario Bros.* and *The Legend of Zelda* with picture, sound, and controllers.

## Status

Working:

- 6502 CPU (all legal opcodes plus the common illegal ones — `LAX`, `SAX`, `DCP`, `ISB`, `SLO`, `RLA`, `SRE`, `RRA`)
- Scanline-accurate PPU with background, sprites (8×8 and 8×16), sprite-0 hit, palette, OAM DMA
- iNES loader
- Mappers: **NROM (0)**, **MMC1 (1)**
- APU: pulse × 2 (with envelope and sweep), triangle, noise, DMC (sample playback); NES hardware filter chain (90 Hz HP → 440 Hz HP → 14 kHz LP)
- Keyboard input, standard-gamepad input (8BitDo, Xbox, DualShock — anything Ebiten recognizes)
- File-open GUI: native dialog, drag-and-drop, recent reset
- Save states (per-ROM `.state` file)

Not yet implemented:

- Other mappers (UxROM/2, CNROM/3, MMC3/4, etc.)
- Battery-backed save RAM persistence (Zelda's in-game save isn't written to disk yet)
- Per-cycle PPU (causes occasional jitter on SMB's status bar split)

## Build

Requires Go 1.22+ and a C toolchain (Ebiten audio uses cgo).

```sh
bash build.sh        # produces balrog.exe (Windows GUI subsystem)
# or:
go build -o balrog.exe .
```

The `build.sh` variant uses `-ldflags "-H=windowsgui"` so double-clicking the exe doesn't pop a console window.

## Run

```sh
./balrog.exe                 # opens with no ROM; pick one from the GUI
./balrog.exe path/to/rom.nes # loads the ROM directly
```

You'll need your own legally-obtained NES ROMs; none are included.

## Controls

### NES controller

| NES button | Keyboard           | Gamepad (Nintendo layout) | Gamepad (Xbox layout) |
|------------|--------------------|---------------------------|-----------------------|
| A          | <kbd>X</kbd>       | A or X                    | B or Y                |
| B          | <kbd>Z</kbd>       | B or Y                    | A or X                |
| Select     | <kbd>R-Shift</kbd> | View / Back               | Back                  |
| Start      | <kbd>Enter</kbd>   | Menu / Start              | Start                 |
| D-pad      | Arrow keys         | D-pad or left stick       | D-pad or left stick   |

### Hotkeys

| Key                       | Action                |
|---------------------------|-----------------------|
| <kbd>F1</kbd> / <kbd>Ctrl</kbd>+<kbd>O</kbd> | Open ROM (file dialog) |
| Drag `.nes` file onto window | Open ROM             |
| <kbd>F2</kbd>             | Save state            |
| <kbd>F4</kbd>             | Load state            |
| <kbd>F5</kbd>             | Reset                 |

State files are written next to the ROM as `<rom-basename>.state`.

## CLI options

For automated testing / scripted playback:

```
--snap   <frame> <path>          capture a PNG snapshot at the given frame
--press  <from> <to> <button>    hold a button across [from, to) frame range
--exit   <frame>                 quit the emulator at this frame
```

Buttons: `A`, `B`, `SELECT`, `START`, `UP`, `DOWN`, `LEFT`, `RIGHT`.

## Project layout

| File          | Contents                                         |
|---------------|--------------------------------------------------|
| `main.go`     | Ebiten frontend, input, file dialog, hotkeys     |
| `nes.go`      | Top-level emulator: CPU/PPU/APU interleaving     |
| `cpu.go`      | 6502 CPU                                         |
| `ppu.go`      | 2C02 PPU (scanline-accurate)                     |
| `apu.go`      | 2A03 APU (pulse, triangle, noise, filter chain)  |
| `bus.go`      | Memory bus, controller registers, OAM DMA        |
| `cart.go`     | iNES loader, mapper interface                    |
| `mapper0.go`  | NROM mapper                                      |
| `mapper1.go`  | MMC1 mapper                                      |
| `state.go`    | Save state serialization (gob)                   |

## Credits

- [NESdev wiki](https://www.nesdev.org/wiki) — canonical reference for everything CPU, PPU, APU, mappers
- [Ebitengine](https://ebitengine.org/) — windowing, input, audio
- [sqweek/dialog](https://github.com/sqweek/dialog) — native file picker
