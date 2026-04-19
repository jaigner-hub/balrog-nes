package main

import (
	"math"
	"sync"
)

// APU: approximates NES 2A03 audio. Implements Pulse1/2, Triangle, Noise,
// length counters, envelope, sweep, and frame counter (mode 0). DMC is
// stubbed (reads/writes accepted but silent).
//
// CPU runs at 1.789773 MHz. APU channels are clocked at CPU rate (pulse/noise
// use pairs — every 2 CPU cycles — but for simplicity we tick each Step).
//
// Output: a ring buffer of float32 samples at sampleRate. The Ebiten audio
// reader pulls from here and converts to 16-bit stereo.

// We match the rate the rest of the emulator actually runs at — 60 ticks per
// second times cpuCyclesPerFrame — so the audio sample rate exactly matches
// the Ebiten output rate. (Real NTSC is 1789773; we're 0.17% slower because
// Ebiten ticks at exactly 60 Hz, not 60.0988 Hz. Matching what we do, not
// what real hardware does, keeps the ring buffer balanced.)
const cpuHz = float64(cpuCyclesPerFrame) * 60.0

var lengthTable = [32]byte{
	10, 254, 20, 2, 40, 4, 80, 6, 160, 8, 60, 10, 14, 12, 26, 14,
	12, 16, 24, 18, 48, 20, 96, 22, 192, 24, 72, 26, 16, 28, 32, 30,
}

var dutyTable = [4][8]byte{
	{0, 1, 0, 0, 0, 0, 0, 0}, // 12.5%
	{0, 1, 1, 0, 0, 0, 0, 0}, // 25%
	{0, 1, 1, 1, 1, 0, 0, 0}, // 50%
	{1, 0, 0, 1, 1, 1, 1, 1}, // 75% (25% negated)
}

var triangleSeq = [32]byte{
	15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0,
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
}

var noisePeriods = [16]uint16{
	4, 8, 16, 32, 64, 96, 128, 160, 202, 254, 380, 508, 762, 1016, 2034, 4068,
}

// NTSC DMC rate table — CPU cycles between output level updates.
var dmcPeriods = [16]uint16{
	428, 380, 340, 320, 286, 254, 226, 214, 190, 160, 142, 128, 106, 84, 72, 54,
}

type envelope struct {
	start       bool
	loop        bool
	constant    bool
	volume      byte // V in $4000
	divider     byte
	decayLevel  byte
}

func (e *envelope) clock() {
	if e.start {
		e.start = false
		e.decayLevel = 15
		e.divider = e.volume
	} else {
		if e.divider == 0 {
			e.divider = e.volume
			if e.decayLevel > 0 {
				e.decayLevel--
			} else if e.loop {
				e.decayLevel = 15
			}
		} else {
			e.divider--
		}
	}
}

func (e *envelope) output() byte {
	if e.constant {
		return e.volume
	}
	return e.decayLevel
}

type pulseCh struct {
	enabled    bool
	channel    byte // 1 or 2 (affects sweep)
	duty       byte
	dutyPos    byte
	timer      uint16
	timerLoad  uint16
	length     byte
	lengthHalt bool
	env        envelope

	// Sweep
	sweepEnabled  bool
	sweepPeriod   byte
	sweepNegate   bool
	sweepShift    byte
	sweepReload   bool
	sweepDivider  byte
}

func (p *pulseCh) writeReg(reg int, v byte) {
	switch reg {
	case 0:
		p.duty = (v >> 6) & 3
		p.lengthHalt = v&0x20 != 0
		p.env.loop = p.lengthHalt
		p.env.constant = v&0x10 != 0
		p.env.volume = v & 0x0F
	case 1:
		p.sweepEnabled = v&0x80 != 0
		p.sweepPeriod = (v >> 4) & 7
		p.sweepNegate = v&0x08 != 0
		p.sweepShift = v & 7
		p.sweepReload = true
	case 2:
		p.timerLoad = (p.timerLoad & 0xFF00) | uint16(v)
	case 3:
		p.timerLoad = (p.timerLoad & 0x00FF) | (uint16(v&7) << 8)
		if p.enabled {
			p.length = lengthTable[(v>>3)&0x1F]
		}
		p.dutyPos = 0
		p.env.start = true
	}
}

func (p *pulseCh) tickTimer() {
	if p.timer == 0 {
		p.timer = p.timerLoad
		p.dutyPos = (p.dutyPos + 1) & 7
	} else {
		p.timer--
	}
}

func (p *pulseCh) clockLength() {
	if !p.lengthHalt && p.length > 0 {
		p.length--
	}
}

func (p *pulseCh) targetPeriod() uint16 {
	change := p.timerLoad >> p.sweepShift
	if p.sweepNegate {
		if p.channel == 1 {
			return p.timerLoad - change - 1
		}
		return p.timerLoad - change
	}
	return p.timerLoad + change
}

func (p *pulseCh) clockSweep() {
	target := p.targetPeriod()
	if p.sweepDivider == 0 && p.sweepEnabled && p.sweepShift > 0 &&
		p.timerLoad >= 8 && target <= 0x7FF {
		p.timerLoad = target
	}
	if p.sweepDivider == 0 || p.sweepReload {
		p.sweepDivider = p.sweepPeriod
		p.sweepReload = false
	} else {
		p.sweepDivider--
	}
}

func (p *pulseCh) output() byte {
	if !p.enabled || p.length == 0 || p.timerLoad < 8 || p.targetPeriod() > 0x7FF {
		return 0
	}
	if dutyTable[p.duty][p.dutyPos] == 0 {
		return 0
	}
	return p.env.output()
}

type triangleCh struct {
	enabled      bool
	timer        uint16
	timerLoad    uint16
	length       byte
	lengthHalt   bool
	linearReload byte
	linearCounter byte
	reloadFlag   bool
	seqPos       byte
}

func (t *triangleCh) writeReg(reg int, v byte) {
	switch reg {
	case 0:
		t.lengthHalt = v&0x80 != 0
		t.linearReload = v & 0x7F
	case 2:
		t.timerLoad = (t.timerLoad & 0xFF00) | uint16(v)
	case 3:
		t.timerLoad = (t.timerLoad & 0x00FF) | (uint16(v&7) << 8)
		if t.enabled {
			t.length = lengthTable[(v>>3)&0x1F]
		}
		t.reloadFlag = true
	}
}

func (t *triangleCh) tickTimer() {
	if t.timer == 0 {
		t.timer = t.timerLoad
		if t.linearCounter > 0 && t.length > 0 {
			t.seqPos = (t.seqPos + 1) & 31
		}
	} else {
		t.timer--
	}
}

func (t *triangleCh) clockLength() {
	if !t.lengthHalt && t.length > 0 {
		t.length--
	}
}

func (t *triangleCh) clockLinear() {
	if t.reloadFlag {
		t.linearCounter = t.linearReload
	} else if t.linearCounter > 0 {
		t.linearCounter--
	}
	if !t.lengthHalt {
		t.reloadFlag = false
	}
}

func (t *triangleCh) output() byte {
	if !t.enabled || t.length == 0 || t.linearCounter == 0 || t.timerLoad < 2 {
		return 0
	}
	return triangleSeq[t.seqPos]
}

// Delta Modulation Channel: plays back 1-bit-delta-encoded PCM samples
// fetched from cartridge memory at $C000-$FFFF. Each output bit nudges the
// 7-bit DAC level up (+2) or down (-2). Used for percussion, voice clips,
// and effects (e.g. Zelda's sword-beam "shing").
type dmcCh struct {
	enabled bool
	irqEn   bool
	loop    bool

	// Configuration registers
	rateIdx     byte
	output      byte // 7-bit DAC value (0..127)
	sampleAddr  uint16
	sampleLen   uint16

	// Playback state
	timer       uint16
	currentAddr uint16
	bytesLeft   uint16

	// Bit-shifter
	shifter        byte
	bitsRemaining  byte
	silence        bool
	sampleBuffer   byte
	bufferLoaded   bool

	irqPending bool
}

func (d *dmcCh) writeReg(reg int, v byte) {
	switch reg {
	case 0: // $4010
		d.irqEn = v&0x80 != 0
		d.loop = v&0x40 != 0
		d.rateIdx = v & 0x0F
		if !d.irqEn {
			d.irqPending = false
		}
	case 1: // $4011
		d.output = v & 0x7F
	case 2: // $4012
		d.sampleAddr = 0xC000 | (uint16(v) << 6)
	case 3: // $4013
		d.sampleLen = (uint16(v) << 4) | 1
	}
}

// fetchByte reads the next sample byte from cartridge memory and stalls the
// CPU 4 cycles (typical DMC DMA cost). Real hardware can stall 1-4 cycles
// depending on what the CPU is doing; 4 is a safe approximation.
func (d *dmcCh) fetchByte(a *APU) {
	if a.bus == nil || d.bytesLeft == 0 {
		return
	}
	d.sampleBuffer = a.bus.Read(d.currentAddr)
	d.bufferLoaded = true
	if d.currentAddr == 0xFFFF {
		d.currentAddr = 0x8000
	} else {
		d.currentAddr++
	}
	d.bytesLeft--
	if d.bytesLeft == 0 {
		if d.loop {
			d.currentAddr = d.sampleAddr
			d.bytesLeft = d.sampleLen
		} else if d.irqEn {
			d.irqPending = true
		}
	}
	if a.cpu != nil {
		a.cpu.stall += 4
	}
}

func (d *dmcCh) startSample() {
	if d.bytesLeft == 0 {
		d.currentAddr = d.sampleAddr
		d.bytesLeft = d.sampleLen
	}
}

// tickTimer is called every CPU cycle. When the timer hits 0, process one
// output bit. If the shifter is empty and a buffer byte is available, refill;
// if no buffer byte, mark this output cycle as "silence" (output stays put).
func (d *dmcCh) tickTimer(a *APU) {
	if d.timer == 0 {
		d.timer = dmcPeriods[d.rateIdx]
		// Process one bit
		if !d.silence {
			if d.shifter&1 != 0 {
				if d.output <= 125 {
					d.output += 2
				}
			} else {
				if d.output >= 2 {
					d.output -= 2
				}
			}
		}
		d.shifter >>= 1
		if d.bitsRemaining > 0 {
			d.bitsRemaining--
		}
		if d.bitsRemaining == 0 {
			d.bitsRemaining = 8
			if !d.bufferLoaded {
				d.silence = true
			} else {
				d.silence = false
				d.shifter = d.sampleBuffer
				d.bufferLoaded = false
				if d.bytesLeft > 0 {
					d.fetchByte(a)
				}
			}
		}
	} else {
		d.timer--
	}
	// Keep the buffer topped up so we don't run out mid-shift.
	if !d.bufferLoaded && d.bytesLeft > 0 {
		d.fetchByte(a)
	}
}

func (d *dmcCh) outputLevel() byte { return d.output }

type noiseCh struct {
	enabled    bool
	timer      uint16
	timerLoad  uint16
	length     byte
	lengthHalt bool
	env        envelope
	mode       bool
	shift      uint16
}

func (n *noiseCh) writeReg(reg int, v byte) {
	switch reg {
	case 0:
		n.lengthHalt = v&0x20 != 0
		n.env.loop = n.lengthHalt
		n.env.constant = v&0x10 != 0
		n.env.volume = v & 0x0F
	case 2:
		n.mode = v&0x80 != 0
		n.timerLoad = noisePeriods[v&0x0F]
	case 3:
		if n.enabled {
			n.length = lengthTable[(v>>3)&0x1F]
		}
		n.env.start = true
	}
}

func (n *noiseCh) tickTimer() {
	if n.timer == 0 {
		n.timer = n.timerLoad
		var bit uint16
		if n.mode {
			bit = (n.shift ^ (n.shift >> 6)) & 1
		} else {
			bit = (n.shift ^ (n.shift >> 1)) & 1
		}
		n.shift = (n.shift >> 1) | (bit << 14)
	} else {
		n.timer--
	}
}

func (n *noiseCh) clockLength() {
	if !n.lengthHalt && n.length > 0 {
		n.length--
	}
}

func (n *noiseCh) output() byte {
	if !n.enabled || n.length == 0 || (n.shift&1) != 0 {
		return 0
	}
	return n.env.output()
}

type APU struct {
	Pulse1, Pulse2 pulseCh
	Triangle       triangleCh
	Noise          noiseCh
	DMC            dmcCh

	cycle      uint64
	frameStep  int
	frameMode  byte // 0=4-step, 1=5-step
	inhibitIRQ bool

	// Set by NewNES so the DMC channel can fetch sample bytes from PRG-ROM
	// (via the bus) and stall the CPU during a DMA fetch.
	bus *NESBus
	cpu *CPU

	// Sample generation
	sampleRate      float64
	cyclesPerSample float64
	sampleCountdown float64

	// Ring buffer (mono float samples) consumed by audio reader
	mu       sync.Mutex
	buf      []float32
	head     int
	tail     int
	filled   int
	capacity int

	// NES hardware-style filter state (1-pole HPs at 90Hz and 440Hz, LP at 14kHz).
	hp1Prev, hp1Out float32
	hp2Prev, hp2Out float32
	lpOut           float32
	hp1Alpha, hp2Alpha, lpAlpha float32
}

func NewAPU(sampleRate float64) *APU {
	a := &APU{
		Pulse1:          pulseCh{channel: 1},
		Pulse2:          pulseCh{channel: 2},
		Noise:           noiseCh{shift: 1},
		sampleRate:      sampleRate,
		cyclesPerSample: cpuHz / sampleRate,
		capacity:        16384,
	}
	a.buf = make([]float32, a.capacity)
	a.sampleCountdown = a.cyclesPerSample
	// 1-pole IIR coeffs; alpha = exp(-2*pi*fc/fs).
	hpAlpha := func(fc float64) float32 {
		return float32(math.Exp(-2 * math.Pi * fc / sampleRate))
	}
	a.hp1Alpha = hpAlpha(90)
	a.hp2Alpha = hpAlpha(440)
	a.lpAlpha = 1 - hpAlpha(14000)
	return a
}

// Register access
func (a *APU) CPUWrite(addr uint16, v byte) {
	switch {
	case addr >= 0x4000 && addr <= 0x4003:
		a.Pulse1.writeReg(int(addr-0x4000), v)
	case addr >= 0x4004 && addr <= 0x4007:
		a.Pulse2.writeReg(int(addr-0x4004), v)
	case addr >= 0x4008 && addr <= 0x400B:
		a.Triangle.writeReg(int(addr-0x4008), v)
	case addr >= 0x400C && addr <= 0x400F:
		a.Noise.writeReg(int(addr-0x400C), v)
	case addr >= 0x4010 && addr <= 0x4013:
		a.DMC.writeReg(int(addr-0x4010), v)
	case addr == 0x4015:
		a.Pulse1.enabled = v&0x01 != 0
		a.Pulse2.enabled = v&0x02 != 0
		a.Triangle.enabled = v&0x04 != 0
		a.Noise.enabled = v&0x08 != 0
		a.DMC.enabled = v&0x10 != 0
		if !a.Pulse1.enabled {
			a.Pulse1.length = 0
		}
		if !a.Pulse2.enabled {
			a.Pulse2.length = 0
		}
		if !a.Triangle.enabled {
			a.Triangle.length = 0
		}
		if !a.Noise.enabled {
			a.Noise.length = 0
		}
		if !a.DMC.enabled {
			a.DMC.bytesLeft = 0
		} else {
			a.DMC.startSample()
		}
		a.DMC.irqPending = false
	case addr == 0x4017:
		a.frameMode = (v >> 7) & 1
		a.inhibitIRQ = v&0x40 != 0
		a.frameStep = 0
		if a.frameMode == 1 {
			a.clockQuarter()
			a.clockHalf()
		}
	}
}

func (a *APU) CPURead(addr uint16) byte {
	if addr == 0x4015 {
		var r byte
		if a.Pulse1.length > 0 {
			r |= 0x01
		}
		if a.Pulse2.length > 0 {
			r |= 0x02
		}
		if a.Triangle.length > 0 {
			r |= 0x04
		}
		if a.Noise.length > 0 {
			r |= 0x08
		}
		if a.DMC.bytesLeft > 0 {
			r |= 0x10
		}
		if a.DMC.irqPending {
			r |= 0x80
		}
		// Reading $4015 acknowledges the DMC IRQ.
		a.DMC.irqPending = false
		return r
	}
	return 0
}

func (a *APU) clockQuarter() {
	a.Pulse1.env.clock()
	a.Pulse2.env.clock()
	a.Noise.env.clock()
	a.Triangle.clockLinear()
}

func (a *APU) clockHalf() {
	a.Pulse1.clockLength()
	a.Pulse1.clockSweep()
	a.Pulse2.clockLength()
	a.Pulse2.clockSweep()
	a.Triangle.clockLength()
	a.Noise.clockLength()
}

// Frame counter — clocks the APU sequencer 4 or 5 times per frame.
// We fire events at approximate CPU-cycle positions (per NESDEV).
func (a *APU) clockFrame() {
	if a.frameMode == 0 {
		switch a.frameStep {
		case 0:
			a.clockQuarter()
		case 1:
			a.clockQuarter()
			a.clockHalf()
		case 2:
			a.clockQuarter()
		case 3:
			a.clockQuarter()
			a.clockHalf()
		}
		a.frameStep = (a.frameStep + 1) & 3
	} else {
		switch a.frameStep {
		case 0, 2:
			a.clockQuarter()
			a.clockHalf()
		case 1, 3:
			a.clockQuarter()
		}
		a.frameStep++
		if a.frameStep == 5 {
			a.frameStep = 0
		}
	}
}

// Step is called once per CPU cycle.
func (a *APU) Step() {
	// Triangle and DMC clocked every CPU cycle.
	a.Triangle.tickTimer()
	a.DMC.tickTimer(a)
	// Pulse/Noise clocked every other CPU cycle (APU rate).
	if a.cycle&1 == 0 {
		a.Pulse1.tickTimer()
		a.Pulse2.tickTimer()
		a.Noise.tickTimer()
	}
	// Frame counter: ~3728.5 CPU cycles between steps (4-step mode)
	// We fire every 7457 CPU cycles (half-frame rate) for simplicity.
	const frameStep = 7457
	if a.cycle%frameStep == 0 && a.cycle > 0 {
		a.clockFrame()
	}
	a.cycle++

	// Produce samples
	a.sampleCountdown--
	if a.sampleCountdown <= 0 {
		a.sampleCountdown += a.cyclesPerSample
		a.emitSample()
	}
}

func (a *APU) emitSample() {
	p1 := a.Pulse1.output()
	p2 := a.Pulse2.output()
	tr := a.Triangle.output()
	no := a.Noise.output()
	// Nonlinear mixer, per NESDEV.
	var pulseOut, tndOut float32
	if p1+p2 > 0 {
		pulseOut = 95.88 / (8128.0/float32(p1+p2) + 100.0)
	}
	dmc := a.DMC.outputLevel()
	denom := float32(tr)/8227.0 + float32(no)/12241.0 + float32(dmc)/22638.0
	if denom > 0 {
		tndOut = 159.79 / (1.0/denom + 100.0)
	}
	s := pulseOut + tndOut
	// NES hardware filter chain: HP 90Hz -> HP 440Hz -> LP 14kHz.
	a.hp1Out = a.hp1Alpha * (a.hp1Out + s - a.hp1Prev)
	a.hp1Prev = s
	a.hp2Out = a.hp2Alpha * (a.hp2Out + a.hp1Out - a.hp2Prev)
	a.hp2Prev = a.hp1Out
	a.lpOut = a.lpOut + a.lpAlpha*(a.hp2Out-a.lpOut)
	a.pushSample(a.lpOut)
}

func (a *APU) pushSample(s float32) {
	a.mu.Lock()
	if a.filled < a.capacity {
		a.buf[a.head] = s
		a.head = (a.head + 1) % a.capacity
		a.filled++
	}
	// On overflow: drop the new sample. Prevents consumer-side discontinuity.
	a.mu.Unlock()
}

// PullSample retrieves one sample, or 0 if buffer is empty.
func (a *APU) PullSample() (float32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.filled == 0 {
		return 0, false
	}
	s := a.buf[a.tail]
	a.tail = (a.tail + 1) % a.capacity
	a.filled--
	return s, true
}
