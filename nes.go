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

// clockMapperScanline fires the mapper's scanline counter at the end of
// each visible scanline (and pre-render) when rendering is enabled. That's
// how MMC3 approximates the PPU-A12 rising-edge count real hardware does.
func (n *NES) clockMapperScanline() {
	if n.scanlineCounter == nil {
		return
	}
	if n.PPU.mask&(maskShowBg|maskShowSpr) == 0 {
		return
	}
	// Previous StepScanline has already advanced PPU.scanline, so the line
	// that just rendered is scanline-1 (mod 262). Fire for visible and
	// pre-render only.
	prev := n.PPU.scanline - 1
	if prev < 0 {
		prev = 261
	}
	if prev < 240 || prev == 261 {
		n.scanlineCounter.ClockScanline()
	}
}

func (n *NES) StepFrame() {
	frameStart := n.CPU.cycles
	// Each iteration covers one 341-PPU-cycle slot (~113.67 CPU cycles).
	// PPU.scanline is the authoritative "what's about to render next"; iter 0
	// is pre-render (PPU.scanline=261), iter 1 renders scanline 0, etc. Use
	// PPU.scanline (not the iteration index) for prediction so we consult v
	// in the state it'll actually have when that scanline renders.
	for i := 0; i < 262; i++ {
		scanlineEnd := frameStart + uint64(cpuCyclesPerFrame*(i+1))/262
		scanlineStart := frameStart + uint64(cpuCyclesPerFrame*i)/262
		target := n.PPU.scanline

		if target < 240 && n.PPU.status&statSpr0 == 0 {
			hitX := n.PPU.predictSprite0Hit(target)
			if hitX >= 0 {
				hitTarget := scanlineStart + uint64(hitX+1)/3
				if hitTarget > scanlineEnd {
					hitTarget = scanlineEnd
				}
				// 1. CPU runs up to the hit cycle (polling, flag not yet set).
				n.runCPUUntil(hitTarget)
				// 2. Render the pre-hit pixels with the current v.
				n.PPU.beginScanlineRender(target)
				n.PPU.renderBGRange(0, hitX+1)
				// 3. Fire sprite-0 hit.
				n.PPU.status |= statSpr0
				// 4. Let CPU run the rest of the scanline's budget.
				n.runCPUUntil(scanlineEnd)
				// 5. Render the post-hit pixels with whatever v is now.
				n.PPU.renderBGRange(hitX+1, 256)
				n.PPU.endScanlineRender(target)
				n.PPU.scanline++
				n.clockMapperScanline()
				continue
			}
		}
		n.runCPUUntil(scanlineEnd)
		done := n.PPU.StepScanline()
		n.clockMapperScanline()
		if done {
			return
		}
	}
}
