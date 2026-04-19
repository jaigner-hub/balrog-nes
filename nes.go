package main

type NES struct {
	Bus *NESBus
	CPU *CPU
	PPU *PPU
	APU *APU
}

func NewNES(cart *Cart, sampleRate float64) *NES {
	ppu := NewPPU(cart)
	apu := NewAPU(sampleRate)
	cpu := &CPU{}
	bus := &NESBus{Cart: cart, PPU: ppu, APU: apu, CPU: cpu}
	cpu.Bus = bus
	apu.bus = bus // DMC reads sample bytes via the bus
	apu.cpu = cpu // DMC adds CPU stall during DMA fetches
	cpu.Reset()
	return &NES{Bus: bus, CPU: cpu, PPU: ppu, APU: apu}
}

// StepFrame runs CPU + PPU + APU until a frame completes.
// NTSC NES runs 29780.67 CPU cycles per frame (262 scanlines * 341 PPU / 3).
// We give each scanline a fractional share of that total so overall CPU
// cycles-per-frame match hardware — critical for audio sample rate to match
// the 44.1 kHz Ebiten output (otherwise the ring buffer drifts and drops).
const cpuCyclesPerFrame = 29780

func (n *NES) runCPUUntil(target uint64) {
	for n.CPU.cycles < target {
		before := n.CPU.cycles
		n.CPU.Step()
		delta := int(n.CPU.cycles - before)
		for i := 0; i < delta; i++ {
			n.APU.Step()
		}
		if n.PPU.NMIPending {
			n.PPU.NMIPending = false
			n.CPU.NMI()
		}
		if n.APU.DMC.irqPending {
			n.CPU.IRQ()
		}
	}
}

func (n *NES) StepFrame() {
	frameStart := n.CPU.cycles
	for scanline := 0; scanline < 262; scanline++ {
		scanlineStart := frameStart + uint64(cpuCyclesPerFrame*scanline)/262
		scanlineEnd := frameStart + uint64(cpuCyclesPerFrame*(scanline+1))/262

		// Visible scanline with sprite-0 hit: split the render in two so the
		// pre-hit pixels use the OLD scroll and the post-hit pixels use the
		// NEW scroll the CPU writes after seeing the flag. This is what real
		// hardware does and is what makes SMB's status-bar split look clean.
		if scanline < 240 && n.PPU.status&statSpr0 == 0 {
			hitX := n.PPU.predictSprite0Hit(scanline)
			if hitX >= 0 {
				hitTarget := scanlineStart + uint64(hitX+1)/3
				if hitTarget > scanlineEnd {
					hitTarget = scanlineEnd
				}
				// 1. CPU runs up to the hit cycle (polling, flag not yet set).
				n.runCPUUntil(hitTarget)
				// 2. Render the pre-hit pixels with the current v.
				n.PPU.beginScanlineRender(scanline)
				n.PPU.renderBGRange(0, hitX+1)
				// 3. Fire sprite-0 hit.
				n.PPU.status |= statSpr0
				// 4. Let CPU run the rest of the scanline's budget; that's
				//    plenty of time for it to detect the flag and write the
				//    new scroll (typically $2000 + $2005+$2005 + $2006+$2006,
				//    ~20-30 CPU cycles).
				n.runCPUUntil(scanlineEnd)
				// 5. Render the post-hit pixels with whatever v is now.
				n.PPU.renderBGRange(hitX+1, 256)
				n.PPU.endScanlineRender(scanline)
				n.PPU.scanline++
				continue
			}
		}
		n.runCPUUntil(scanlineEnd)
		if n.PPU.StepScanline() {
			return
		}
	}
}
