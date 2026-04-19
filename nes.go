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

		// On visible scanlines: predict sprite-0 hit and pre-set the flag at
		// the right cycle so the CPU's $2002 polling loop sees it mid-scanline,
		// not on the next scanline. This mirrors hardware timing closely enough
		// to stop the SMB status-bar split from wandering.
		if scanline < 240 && n.PPU.status&statSpr0 == 0 {
			if hitX := n.PPU.predictSprite0Hit(scanline); hitX >= 0 {
				// PPU pixel X corresponds to PPU cycle X+1, which is CPU cycle
				// (X+1)/3 into the scanline.
				hitTarget := scanlineStart + uint64(hitX+1)/3
				if hitTarget > scanlineEnd {
					hitTarget = scanlineEnd
				}
				n.runCPUUntil(hitTarget)
				n.PPU.status |= statSpr0
			}
		}
		n.runCPUUntil(scanlineEnd)
		if n.PPU.StepScanline() {
			return
		}
	}
}
