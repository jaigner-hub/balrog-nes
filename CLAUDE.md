# Working on balrog

Notes for future Claude sessions on this codebase. Keeps context across
sessions so the same mistakes don't get made twice. Update it when
recurring gotchas appear.

## Build

**Always build with `-ldflags "-H=windowsgui"`.** Two wrappers do this:

- `bash build.sh` — works from the Bash tool (Git Bash on Windows).
- `cmd //c ".\\build.bat"` — works from cmd.exe or PowerShell. Git Bash
  can still shell into it with the explicit `.\` prefix shown.

Both produce `balrog.exe` with the Windows GUI subsystem so Explorer
launches don't pop a console window.

A plain `go build -o balrog.exe .` works for quick dev iteration but:

- It defaults to the Console subsystem.
- `console_windows.go` calls `FreeConsole()` as a fallback, but the console
  still **flashes visibly** for a few hundred milliseconds before Go's
  runtime detaches. This is noticeable and annoying.
- Never produce a release binary this way — and don't leave one in the
  repo root either, because the user might double-click it.

Verify subsystem on the built exe with PowerShell:

```powershell
$bytes = [System.IO.File]::ReadAllBytes('balrog.exe')
$peoff = [System.BitConverter]::ToInt32($bytes, 0x3C)
[System.BitConverter]::ToInt16($bytes, $peoff + 0x5C)  # 2=GUI, 3=Console
```

## File layout

| File                    | What's in it                                   |
|-------------------------|------------------------------------------------|
| `main.go`               | Ebiten Game loop, CLI flags, hotkeys, drag-drop |
| `menubar.go`            | Top-of-window menu bar (File / Emulation / Help) |
| `input.go`              | Rebindable input config + "Configure Input" dialog |
| `console_windows.go`    | Windows-only: FreeConsole fallback for raw `go build` |
| `nes.go`                | Top-level NES wiring: CPU tick → PPU/APU       |
| `cpu.go`                | 6502 CPU, cycle-accurate                       |
| `ppu.go`                | 2C02 PPU, cycle-accurate                       |
| `apu.go`                | 2A03 APU                                        |
| `bus.go`                | Memory bus, controller I/O, OAM DMA             |
| `cart.go`               | iNES loader, Mapper interface                  |
| `mapper{0,1,2,3,4,7,11}.go` | Per-mapper code                             |
| `state.go`              | Save-state serialization (gob), per-mapper blobs |
| `nestest_trace.go`      | nestest automation driver for CPU validation   |

Diagnostic tools under `tools/`:

- `tools/crc_check`   — computes blargg CRC-32 against a byte stream
- `tools/framediff`   — per-scanline pixel diff between two PNGs
- `tools/pixcompare`  — side-by-side pixel RGB comparison
- `tools/pixdump`     — single-row RGB dump
- `tools/rowmap`      — compact B/W/dot row map
- `tools/zoomband`    — crop + 4× upscale a horizontal band
- `tools/zoomtop`     — 4× zoom of the top of a frame

## CLI flags worth knowing

```
--snap         <frame> <path>        save the NES frame (256×240) as PNG
--exit         <frame>               quit at frame
--press        <from> <to> <button>  inject controller input over a range
--load-state                         auto-load the ROM's .state file at startup
--trace        <rom> <out.log>       nestest-automation trace (CPU validation)
--test-dialog  <frame> <path>        force Configure Input dialog open + full-window snapshot (dev)
```

`--snap` captures only the PPU output (no menu/overlay). For UI
screenshots including the menu bar or a dialog, use `--test-dialog`
(saves the full Ebiten screen via `ReadPixels`), or drive the running
emulator via computer-use (see "Screenshots for README" below).

## Testing

Canonical test ROMs live under `testroms/nes-test-roms/` (a submodule
clone of https://github.com/christopherpow/nes-test-roms).

Quick "does everything still work?" pass after any CPU/PPU/APU change:

```sh
# nestest trace — must match exactly
./balrog.exe --trace testroms/nes-test-roms/other/nestest.nes /tmp/nestest.log
diff <(tr -d '\r' < /tmp/nestest.log) <(tr -d '\r' < nestest.log)
# expect only the handful of pre-existing APU-register diffs (lines ~8981+)

# CPU instruction tests (individuals pass; all_instrs.nes reportedly hangs)
for i in 01 02 03 04 05 06 07 08 09 10 11 12 13 14 15 16; do
  ./balrog.exe testroms/nes-test-roms/instr_test-v5/rom_singles/${i}-*.nes \
    --exit 600 --snap 580 /tmp/instr${i}.png
done

# ppu_vbl_nmi — 8/10 pass
for t in 01 02 03 04 05 06 07 08 09 10; do
  ./balrog.exe testroms/nes-test-roms/ppu_vbl_nmi/rom_singles/${t}-*.nes \
    --exit 600 --snap 580 /tmp/vbl${t}.png
done

# Real-game sanity: SMB, SMB3, Zelda, Battletoads should boot to title.
```

### Known-failing tests (accepted)

Do NOT spend time trying to fix these unless specifically asked:

| Test                               | Why it fails                                         |
|------------------------------------|------------------------------------------------------|
| `ppu_vbl_nmi` **07** nmi_on_timing  | Display matches expected pattern (looks right by eye) but CRC differs — there's an extra "N" iteration at the VBL-clear boundary that's visually indistinguishable from "-". Needs sub-PPU-cycle NMI modeling we don't have. |
| `ppu_vbl_nmi` **10** even_odd_timing | Subtests 3 and 5 probe a 1-PPU-cycle boundary when enabling/disabling BG around the odd-frame skip. CPU-write-to-PPU-mask ordering can't express it. |
| `mmc3_test_2` **4** scanline_timing | Cycle-exact IRQ timing, same sub-cycle class.         |
| `mmc3_test_2` **6** MMC3_alt        | Tests Rev A chip variant — different silicon, not a bug. |

Our model is "writes take effect atomically at the CPU cycle boundary" with
3 PPU sub-ticks per cycle; these tests probe races at finer granularity than
that. Accurate enough for every real game we've tried.

## CPU/PPU interleaving gotchas

- `cpu.tick()` is called from `read/write/internal cycle` helpers. It
  increments `cycles`, invokes `tickFn` (set in `NewNES`), then updates
  interrupt latches.
- `tickFn` runs `APU.Step()` then `PPU.Step()` three times, **then**
  samples `PPU.NMIPending` into `CPU.rawNMI`. **NMI is sampled after
  the 1st of the 3 PPU sub-ticks** (not after all 3). That's the
  phi1-ish sample point blargg's 05-nmi_timing expects.
- NMI pipeline: `rawNMI → nmiLatch → nmiPend → Step()`. That's a 1-cycle
  latch and a 1-cycle "polled at next instruction boundary" delay.
- `ClearPendingNMI()` wipes all stages (used by $2002 VBL-race).
  `DeassertNMI()` only drops the raw line (used by $2000 NMI enable
  1→0 write) so an already-sampled NMI still fires.
- `CPUWrite` runs BEFORE the 3 PPU sub-ticks for its cycle. So the
  write's effect is visible to all 3 sub-ticks, but VBL/NMI state
  changes caused by the write happen at "the start of this CPU cycle",
  not spread across it.

## MMC3

- Scanline IRQ clocked by **real A12 rising edges** on CHR fetches,
  not a cycle-count hack. See `observeA12` in `ppu.go`.
- 12-PPU-cycle low-time filter on A12 to avoid spurious clocks.
- `ppu.go`'s "odd-frame cycle skip" checks at cy=339. Moving it to
  cy=340 was tried and breaks test-2 of even_odd_timing, so leave it
  at 339.

## UI / Ebiten

- `Game.Layout` returns `(ow, oh)` so logical pixels = device pixels.
- Mouse coords from `ebiten.CursorPosition()` are in the Layout space.
  Hit-test against `screen.Bounds()` in Draw; the dialog caches its
  layout in `draw()` and the cached rects are consumed in `update()`.
- The menu bar and input dialog both consume clicks so they don't
  leak through to the NES controller byte. The input dialog also
  suppresses ALL input reading while it's in "capture" mode (see
  `InputConfig.readController` being gated in `Game.Update`).
- Input bindings live in `balrog.cfg` (JSON) next to the exe.

## Save states

- 10 slots (0–9). Slot 0 = `<rom>.state` (legacy name); slots 1–9 =
  `<rom>.state1..9`. Backward compatible with saves from before v0.4.
- F2 = save, F4 = load, F6 = prev slot, F7 = next slot.
- Active slot appears in the window title when non-zero.

## Screenshots for README

`--snap` only captures the 256×240 NES frame. For a screenshot that
includes the menu bar, title bar, or an open menu dropdown — e.g. to
update `docs/smb3-window.png` — use computer-use:

1. Launch `balrog.exe <rom>` in the background via Bash.
2. `request_access` with app name `"balrog.exe"`.
3. Interact via `left_click` / `key` to open the desired menu.
4. Capture via PowerShell from Bash:
   ```bash
   cat > /tmp/cap.ps1 <<'EOF'
   Add-Type -AssemblyName System.Windows.Forms,System.Drawing
   $bmp = New-Object System.Drawing.Bitmap 760, 765
   $gfx = [System.Drawing.Graphics]::FromImage($bmp)
   $gfx.CopyFromScreen(1078, 210, 0, 0, $bmp.Size)
   $bmp.Save("C:\path\to\out.png")
   EOF
   powershell -File /tmp/cap.ps1
   ```
   Native display resolution here is 2560×1440. The computer-use
   screenshot tool returns a ~1456×820 downsampled image, so coords
   you read off that screenshot need scaling by ~1.76 before feeding
   to `CopyFromScreen`.
5. `taskkill //F //IM balrog.exe` to clean up.

## Release process

Tag + release is fully automated once a version is ready:

```bash
export PATH="$PATH:/c/Program Files/GitHub CLI"
git tag vX.Y -m "release notes..."
bash build.sh                     # GUI subsystem
./balrog.exe mario.nes --exit 300 --snap 250 /tmp/smoke.png  # sanity
git push origin master
git push origin vX.Y
git tag -l --format='%(contents)' vX.Y > /tmp/notes.md
gh release create vX.Y balrog.exe \
  --title "balrog vX.Y — ..." \
  --notes-file /tmp/notes.md
```

`gh` is installed to `C:\Program Files\GitHub CLI\gh.exe`. PATH usually
isn't set in fresh bash shells so prefix commands with the export
shown above.

## Don't do this

- Don't introduce new constants tuned to a specific game's timing. The
  project's rule is to use hardware-accurate mechanisms (real A12 edges,
  not cycle-count hacks). `mmc3ClockCy = 200` is the one exception and
  it's explicitly documented in the README.
- Don't revert line endings to CRLF. The repo uses LF; git warns but
  the files stay LF on disk.
- Don't commit `balrog.cfg` or `.state` files — they're user-local.
- Don't commit ROMs (none are checked in; keep it that way).
