package main

// Optional interfaces a mapper can implement to participate in scanline-based
// IRQ counting (used by MMC3 and its cousins). We type-assert at runtime, so
// older mappers that don't need this don't have to implement it.
type scanlineCounter interface {
	ClockScanline()
}
type irqMapper interface {
	IRQPending() bool
}

type NES struct {
	Bus *NESBus
	CPU *CPU
	PPU *PPU
	APU *APU

	// Cached type assertions against the current cart's mapper. nil if the
	// mapper doesn't implement the corresponding interface.
	scanlineCounter scanlineCounter
	irqMapper       irqMapper
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
	n := &NES{Bus: bus, CPU: cpu, PPU: ppu, APU: apu}
	if sc, ok := cart.mapper.(scanlineCounter); ok {
		n.scanlineCounter = sc
	}
	if irq, ok := cart.mapper.(irqMapper); ok {
		n.irqMapper = irq
	}
	return n
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
		if n.irqMapper != nil && n.irqMapper.IRQPending() {
			n.CPU.IRQ()
		}
	}
}

func (n *NES) StepFrame() {
	frameStart := n.CPU.cycles
	// Each iteration covers one 341-PPU-cycle slot (~113.67 CPU cycles).
	// PPU.scanline is the authoritative "what's about to render next"; iter 0
	// is pre-render (PPU.scanline=261), iter 1 renders scanline 0, etc.
	//
	// Real MMC3 fires its scanline IRQ on the first PPU A12 rising edge around
	// PPU cycle 260 of visible scanlines (when sprite fetching begins, with
	// BG at $0000 / sprites at $1000). That lands at roughly CPU cycle 87 of
	// the scanline's ~113.67 — about 77% through. Clocking mid-scanline gives
	// the IRQ handler enough cycles left in the same scanline to write new
	// scroll registers, matching how MMC3 games expect things to line up.
	for i := 0; i < 262; i++ {
		scanlineStart := frameStart + uint64(cpuCyclesPerFrame*i)/262
		scanlineEnd := frameStart + uint64(cpuCyclesPerFrame*(i+1))/262
		target := n.PPU.scanline
		rendering := n.PPU.mask&(maskShowBg|maskShowSpr) != 0
		visible := target < 240
		preRender := target == 261

		// Sprite-0 hit with a top-of-screen split. Gated to scanlines < 48 so
		// gameplay sprites (e.g., Mario-as-sprite-0 in SMB3) don't trigger a
		// disruptive mid-scanline split every frame.
		if visible && target < 48 && n.PPU.status&statSpr0 == 0 {
			if hitX := n.PPU.predictSprite0Hit(target); hitX >= 0 {
				hitTarget := scanlineStart + uint64(hitX+1)/3
				if hitTarget > scanlineEnd {
					hitTarget = scanlineEnd
				}
				n.runCPUUntil(hitTarget)
				n.PPU.beginScanlineRender(target)
				n.PPU.renderBGRange(0, hitX+1)
				n.PPU.status |= statSpr0
				n.runCPUUntil(scanlineEnd)
				n.PPU.renderBGRange(hitX+1, 256)
				n.PPU.endScanlineRender(target)
				n.PPU.scanline++
				if rendering && n.scanlineCounter != nil {
					n.scanlineCounter.ClockScanline()
				}
				continue
			}
		}

		// Mid-scanline MMC3 IRQ clock for visible scanlines. Firing before
		// the CPU's scanline budget runs out gives SMB3 / Kirby / Mega Man 3+
		// IRQ handlers time to complete the scroll-register writes they need.
		if visible && rendering && n.scanlineCounter != nil {
			mid := scanlineStart + (scanlineEnd-scanlineStart)*87/114
			n.runCPUUntil(mid)
			n.scanlineCounter.ClockScanline()
		}

		n.runCPUUntil(scanlineEnd)
		done := n.PPU.StepScanline()

		// Pre-render still gets a scanline clock (it's one of MMC3's 241
		// clocks per frame); doing it at end-of-iter is close enough to the
		// real PPU-cycle-324 spot.
		if preRender && rendering && n.scanlineCounter != nil {
			n.scanlineCounter.ClockScanline()
		}
		if done {
			return
		}
	}
}
