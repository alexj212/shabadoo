package main

// A minimal QR encoder, so a phone can pair by camera instead of someone
// reading eight hex characters aloud.
//
// This is the "few hundred lines" CLAUDE.md said a QR would cost, written out
// rather than pulled in: the coordinator is stdlib-only and one convenience
// does not justify breaking that. It is deliberately the smallest encoder that
// does the job — byte mode, error-correction level M, versions 1..10 — which
// covers a pairing URL of ~60 characters with room to spare. No numeric or
// alphanumeric modes, no multi-segment optimisation, no version 40. If a
// payload ever outgrows this it should get shorter, not this file bigger.
//
// Reference: ISO/IEC 18004. The tables below are from the standard; the parts
// worth understanding are marked.

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Galois field arithmetic (GF(256), the field QR's Reed-Solomon lives in)
// ---------------------------------------------------------------------------

var (
	gfExp [512]byte // antilog table, doubled so index arithmetic never wraps
	gfLog [256]byte
)

func init() {
	// x^8 + x^4 + x^3 + x^2 + 1 = 0x11d, the primitive polynomial QR uses.
	x := 1
	for i := range 255 {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGenerator builds the generator polynomial for n error-correction bytes:
// (x-2^0)(x-2^1)...(x-2^(n-1)).
func rsGenerator(n int) []byte {
	g := []byte{1}
	for i := range n {
		// Multiply g by (x + 2^i). Coefficients run highest degree first, so
		// the x term keeps its index and the constant term shifts one right.
		// Doing this the other way round builds g*(2^i*x + 1) — a polynomial
		// that looks just as plausible and produces error-correction bytes no
		// scanner accepts.
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		g = next
	}
	return g
}

// rsEncode returns the n error-correction bytes for data.
func rsEncode(data []byte, n int) []byte {
	gen := rsGenerator(n)
	rem := make([]byte, len(data)+n)
	copy(rem, data)

	for i := range data {
		lead := rem[i]
		if lead == 0 {
			continue
		}
		for j, c := range gen {
			rem[i+j] ^= gfMul(c, lead)
		}
	}
	return rem[len(data):]
}

// ---------------------------------------------------------------------------
// Version tables (level M only)
// ---------------------------------------------------------------------------

// qrVersion describes one symbol version at EC level M.
type qrVersion struct {
	version   int
	dataBytes int // total data codewords
	ecPerBlk  int // EC codewords per block
	group1    int // number of blocks in group 1
	blk1      int // data codewords in each group-1 block
	group2    int // number of blocks in group 2 (0 if none)
	blk2      int // data codewords in each group-2 block
}

// Versions 1..10 at level M. Enough for a pairing URL; see the file comment.
var qrVersionsM = []qrVersion{
	{1, 16, 10, 1, 16, 0, 0},
	{2, 28, 16, 1, 28, 0, 0},
	{3, 44, 26, 1, 44, 0, 0},
	{4, 64, 18, 2, 32, 0, 0},
	{5, 86, 24, 2, 43, 0, 0},
	{6, 108, 16, 4, 27, 0, 0},
	{7, 124, 18, 4, 31, 0, 0},
	{8, 154, 22, 2, 38, 2, 39},
	{9, 182, 22, 3, 36, 2, 37},
	{10, 216, 26, 4, 43, 1, 44},
}

// alignmentCentres[v] lists alignment-pattern row/column centres for version v.
var alignmentCentres = map[int][]int{
	1: nil,
	2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30},
	6: {6, 34}, 7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50},
}

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

type bitBuf struct {
	bits []bool
}

func (b *bitBuf) add(value, n int) {
	for i := n - 1; i >= 0; i-- {
		b.bits = append(b.bits, value&(1<<i) != 0)
	}
}

// pickVersion returns the smallest version whose data capacity holds n bytes in
// byte mode, accounting for the mode indicator and length field.
func pickVersion(n int) (qrVersion, error) {
	for _, v := range qrVersionsM {
		lenBits := 8
		if v.version >= 10 {
			lenBits = 16
		}
		if 4+lenBits+n*8 <= v.dataBytes*8 {
			return v, nil
		}
	}
	return qrVersion{}, fmt.Errorf("payload of %d bytes is too long for this encoder "+
		"(max ~%d); shorten the URL rather than extending the encoder", n, qrVersionsM[len(qrVersionsM)-1].dataBytes-3)
}

// encodeData produces the final codeword stream: mode + length + payload +
// terminator + padding, then Reed-Solomon, interleaved as the spec requires.
func encodeData(payload []byte) (qrVersion, []byte, error) {
	v, err := pickVersion(len(payload))
	if err != nil {
		return v, nil, err
	}

	var b bitBuf
	b.add(0b0100, 4) // byte mode
	lenBits := 8
	if v.version >= 10 {
		lenBits = 16
	}
	b.add(len(payload), lenBits)
	for _, c := range payload {
		b.add(int(c), 8)
	}

	// Terminator: up to four zero bits, truncated if capacity runs out first.
	capacity := v.dataBytes * 8
	for range 4 {
		if len(b.bits) >= capacity {
			break
		}
		b.add(0, 1)
	}
	// Then pad with zeros to a byte boundary.
	for len(b.bits)%8 != 0 {
		b.add(0, 1)
	}

	data := make([]byte, 0, v.dataBytes)
	for i := 0; i < len(b.bits); i += 8 {
		var c byte
		for j := range 8 {
			if b.bits[i+j] {
				c |= 1 << (7 - j)
			}
		}
		data = append(data, c)
	}
	// Alternating pad bytes, per the spec.
	for i := 0; len(data) < v.dataBytes; i++ {
		if i%2 == 0 {
			data = append(data, 0xEC)
		} else {
			data = append(data, 0x11)
		}
	}

	// Split into blocks, compute EC per block, then interleave.
	var blocks, ecBlocks [][]byte
	pos := 0
	for range v.group1 {
		blocks = append(blocks, data[pos:pos+v.blk1])
		pos += v.blk1
	}
	for range v.group2 {
		blocks = append(blocks, data[pos:pos+v.blk2])
		pos += v.blk2
	}
	for _, blk := range blocks {
		ecBlocks = append(ecBlocks, rsEncode(blk, v.ecPerBlk))
	}

	var out []byte
	maxData := max(v.blk1, v.blk2)
	for i := range maxData {
		for _, blk := range blocks {
			if i < len(blk) {
				out = append(out, blk[i])
			}
		}
	}
	for i := range v.ecPerBlk {
		for _, blk := range ecBlocks {
			out = append(out, blk[i])
		}
	}
	return v, out, nil
}

// ---------------------------------------------------------------------------
// Matrix construction
// ---------------------------------------------------------------------------

type qrMatrix struct {
	size int
	mod  []bool // module is dark
	set  []bool // module is function-pattern (reserved, never masked)
}

func newMatrix(size int) *qrMatrix {
	return &qrMatrix{size: size, mod: make([]bool, size*size), set: make([]bool, size*size)}
}

func (m *qrMatrix) at(x, y int) bool       { return m.mod[y*m.size+x] }
func (m *qrMatrix) reserved(x, y int) bool { return m.set[y*m.size+x] }

func (m *qrMatrix) put(x, y int, dark, reserve bool) {
	m.mod[y*m.size+x] = dark
	if reserve {
		m.set[y*m.size+x] = true
	}
}

func (m *qrMatrix) finder(x, y int) {
	for dy := -1; dy <= 7; dy++ {
		for dx := -1; dx <= 7; dx++ {
			px, py := x+dx, y+dy
			if px < 0 || py < 0 || px >= m.size || py >= m.size {
				continue
			}
			inRing := (dx == 0 || dx == 6) && dy >= 0 && dy <= 6
			inRing = inRing || ((dy == 0 || dy == 6) && dx >= 0 && dx <= 6)
			inCore := dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
			m.put(px, py, inRing || inCore, true)
		}
	}
}

func (m *qrMatrix) alignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			dark := dx == -2 || dx == 2 || dy == -2 || dy == 2 || (dx == 0 && dy == 0)
			m.put(cx+dx, cy+dy, dark, true)
		}
	}
}

// buildMatrix lays down every function pattern and returns the matrix with data
// regions still empty.
func buildMatrix(v qrVersion) *qrMatrix {
	size := v.version*4 + 17
	m := newMatrix(size)

	m.finder(0, 0)
	m.finder(size-7, 0)
	m.finder(0, size-7)

	// Timing patterns.
	for i := 8; i < size-8; i++ {
		dark := i%2 == 0
		m.put(i, 6, dark, true)
		m.put(6, i, dark, true)
	}

	// Alignment patterns, skipping those that would collide with a finder.
	centres := alignmentCentres[v.version]
	for _, cy := range centres {
		for _, cx := range centres {
			if (cx == 6 && cy == 6) || (cx == 6 && cy == size-7) || (cx == size-7 && cy == 6) {
				continue
			}
			m.alignment(cx, cy)
		}
	}

	// Dark module, and reserved format-information areas.
	m.put(8, size-8, true, true)
	for i := range 9 {
		if !m.reserved(i, 8) {
			m.put(i, 8, false, true)
		}
		if !m.reserved(8, i) {
			m.put(8, i, false, true)
		}
	}
	for i := range 8 {
		m.put(size-1-i, 8, false, true)
		m.put(8, size-1-i, false, true)
	}

	// Version information: 18 bits, in two places, on version 7 and up only.
	//
	// Omitting this was a silent defect: versions 1-6 do not carry it, so every
	// short payload encoded correctly and every long one produced a symbol that
	// renders perfectly and scans as nothing. The pairing URL crosses into
	// version 7 at 107 bytes — which a MagicDNS coordinator host plus a device
	// label reaches easily — so it was reachable in normal use, not a corner.
	if bits, ok := versionInfoBits[v.version]; ok {
		for i := range 18 {
			dark := bits>>uint(i)&1 == 1
			// Bottom-left 3x6 and top-right 6x3, mirrored about the diagonal.
			m.put(i/3, size-11+i%3, dark, true)
			m.put(size-11+i%3, i/3, dark, true)
		}
	}
	return m
}

// versionInfoBits is the 18-bit version information (6 data bits + 12 BCH
// check bits) for the versions this encoder emits.
//
// A table rather than a BCH implementation: only four values are reachable
// here, they are fixed by ISO/IEC 18004 Annex D, and a table cannot be subtly
// wrong in a way that a hand-rolled polynomial division can. Versions 1-6 are
// deliberately ABSENT — they must not carry this block at all, and a lookup
// miss is how that is expressed.
var versionInfoBits = map[int]int{
	7:  0x07C94,
	8:  0x085BC,
	9:  0x09A99,
	10: 0x0A4D3,
}

// placeData walks the zig-zag column order and writes the codeword bits.
func placeData(m *qrMatrix, data []byte) {
	bit := 0
	total := len(data) * 8
	up := true

	for right := m.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // the vertical timing pattern column is skipped
		}
		for i := range m.size {
			y := i
			if up {
				y = m.size - 1 - i
			}
			for dx := range 2 {
				x := right - dx
				if m.reserved(x, y) {
					continue
				}
				dark := false
				if bit < total {
					dark = data[bit/8]&(1<<(7-bit%8)) != 0
					bit++
				}
				m.put(x, y, dark, false)
			}
		}
		up = !up
	}
}

// maskFn returns whether module (x,y) is flipped by mask pattern n.
func maskFn(n, x, y int) bool {
	switch n {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return (x*y)%2+(x*y)%3 == 0
	case 6:
		return ((x*y)%2+(x*y)%3)%2 == 0
	default:
		return ((x+y)%2+(x*y)%3)%2 == 0
	}
}

// formatBits returns the 15-bit format information for level M and mask n,
// including its BCH error correction and the fixed XOR mask.
func formatBits(mask int) int {
	const levelM = 0b00
	data := levelM<<3 | mask
	rem := data << 10
	for i := 14; i >= 10; i-- {
		if rem&(1<<i) != 0 {
			rem ^= 0b10100110111 << (i - 10)
		}
	}
	return ((data << 10) | rem) ^ 0b101010000010010
}

// penalty scores a masked matrix; the spec picks the lowest-scoring mask. Only
// the first three rules are implemented — the fourth (dark-module balance)
// changes the choice rarely and never changes whether a scanner can read it.
func penalty(m *qrMatrix) int {
	score := 0
	// Rule 1: runs of five or more same-coloured modules in a row or column.
	for _, byRow := range []bool{true, false} {
		for a := range m.size {
			run, last := 0, false
			for b := range m.size {
				var v bool
				if byRow {
					v = m.at(b, a)
				} else {
					v = m.at(a, b)
				}
				if b > 0 && v == last {
					run++
					if run == 5 {
						score += 3
					} else if run > 5 {
						score++
					}
				} else {
					run = 1
				}
				last = v
			}
		}
	}
	// Rule 2: 2x2 blocks of one colour.
	for y := range m.size - 1 {
		for x := range m.size - 1 {
			v := m.at(x, y)
			if v == m.at(x+1, y) && v == m.at(x, y+1) && v == m.at(x+1, y+1) {
				score += 3
			}
		}
	}
	return score
}

// Encode renders payload as a QR matrix: true means a dark module.
//
// All eight masks are built and the lowest-penalty one wins, as the standard
// requires. Any of them is a VALID symbol — two encoders that disagree on mask
// choice both produce readable QRs, which is why the test compares against a
// reference encoder mask-by-mask rather than expecting one exact matrix.
func Encode(payload string) ([][]bool, error) {
	v, data, err := encodeData([]byte(payload))
	if err != nil {
		return nil, err
	}

	best := -1
	var bestGrid *qrMatrix
	for mask := range 8 {
		m := encodeWithMask(v, data, mask)
		if p := penalty(m); best < 0 || p < best {
			best, bestGrid = p, m
		}
	}
	return gridOf(bestGrid), nil
}

func gridOf(m *qrMatrix) [][]bool {
	grid := make([][]bool, m.size)
	for y := range grid {
		grid[y] = make([]bool, m.size)
		for x := range grid[y] {
			grid[y][x] = m.at(x, y)
		}
	}
	return grid
}

// encodeWithMask builds the full symbol for one specific mask.
func encodeWithMask(v qrVersion, data []byte, mask int) *qrMatrix {
	{
		m := buildMatrix(v)
		placeData(m, data)

		// Apply the mask to data modules only.
		for y := range m.size {
			for x := range m.size {
				if !m.reserved(x, y) && maskFn(mask, x, y) {
					m.mod[y*m.size+x] = !m.mod[y*m.size+x]
				}
			}
		}
		// Format information, written twice so a damaged corner still reads.
		//
		// The mapping is not symmetric and the two copies run in opposite
		// directions, which is exactly the kind of thing that produces a symbol
		// that looks perfect and scans as nothing. `put` takes (x, y) — getting
		// those the wrong way round was the original bug here, and the only
		// reason it was caught is the reference-encoder test.
		f := formatBits(mask)
		bit := func(i int) bool { return f&(1<<i) != 0 }
		size := m.size

		// Copy 1, around the top-left finder.
		for i := range 6 {
			m.put(8, i, bit(i), true) // column 8, rows 0..5
		}
		m.put(8, 7, bit(6), true)
		m.put(8, 8, bit(7), true)
		m.put(7, 8, bit(8), true)
		for i := 9; i < 15; i++ {
			m.put(14-i, 8, bit(i), true) // row 8, columns 5..0
		}

		// Copy 2, split between the other two finders.
		for i := range 8 {
			m.put(size-1-i, 8, bit(i), true) // row 8, columns size-1..size-8
		}
		for i := 8; i < 15; i++ {
			m.put(8, size-15+i, bit(i), true) // column 8, rows size-7..size-1
		}
		m.put(8, size-8, true, true) // the module that is always dark

		return m
	}
}

// RenderTerminal draws a QR using half-block characters, so a version-3 symbol
// fits a normal terminal window instead of needing 60 lines. Two matrix rows
// share one text row: the foreground is the upper module, the background the
// lower one.
//
// Colours are inverted from the obvious: QR readers expect DARK modules on a
// LIGHT background, and a terminal's default background is usually dark. Drawing
// light-on-dark produces a symbol most phones will not read, which is a
// frustrating thing to debug — so the quiet zone and light modules are
// explicitly white.
func RenderTerminal(grid [][]bool) string {
	// BOTH colours are set on every cell, always.
	//
	// The first version set only a background and relied on the block character
	// for the other half — but `▄` paints its lower half in the FOREGROUND, and
	// the foreground was never set, so half of every mixed cell rendered in
	// whatever colour the terminal happened to use for text. On a dark theme
	// the light-over-dark cells came out inverted; on a light theme the
	// dark-over-light ones did. Either way roughly a quarter of the modules
	// were wrong and the symbol would not scan — while still looking
	// convincingly like a QR code, which is why it survived review.
	//
	// Never rely on a terminal's defaults for something a camera has to read.
	const (
		lightOnDark = "\033[30;47m" // black text on white  → ▄ lower half dark
		darkOnLight = "\033[37;40m" // white text on black  → ▄ lower half light
		allDark     = "\033[40m"
		allLight    = "\033[47m"
		reset       = "\033[0m"
	)
	size := len(grid)
	quiet := 4 // the standard requires a 4-module quiet zone; scanners rely on it

	dark := func(x, y int) bool {
		x -= quiet
		y -= quiet
		if x < 0 || y < 0 || x >= size || y >= size {
			return false
		}
		return grid[y][x]
	}

	var b strings.Builder
	full := size + quiet*2
	// A QR is always an odd number of modules, and the quiet zone keeps it odd,
	// so the final row is always a half row. dark() reports light beyond the
	// grid, which is what the quiet zone should be anyway.
	for y := 0; y < full; y += 2 {
		for x := range full {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString(allDark + " " + reset)
			case top: // upper dark, lower light
				b.WriteString(darkOnLight + "\u2584" + reset)
			case bottom: // upper light, lower dark
				b.WriteString(lightOnDark + "\u2584" + reset)
			default:
				b.WriteString(allLight + " " + reset)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// RenderSVG draws a QR as an SVG, for the pairing page — the terminal renderer
// uses block characters, which a browser cannot show.
//
// One <path> of rectangles rather than one element per module: a version-3
// symbol is over a thousand modules, and a thousand <rect> elements is a
// noticeably heavy document for something displayed at 200 pixels.
//
// The quiet zone is included and the background drawn explicitly white. Scanners
// need both, and the page behind this is dark — a QR rendered transparent onto a
// dark background is the classic "why won't it scan" that costs an afternoon.
func RenderSVG(payload string) ([]byte, error) {
	grid, err := Encode(payload)
	if err != nil {
		return nil, err
	}
	const quiet = 4
	size := len(grid) + quiet*2

	var d strings.Builder
	for y, row := range grid {
		for x, dark := range row {
			if dark {
				fmt.Fprintf(&d, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`shape-rendering="crispEdges" role="img" aria-label="pairing QR code">`, size, size)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, size, size)
	fmt.Fprintf(&b, `<path d="%s" fill="#000"/></svg>`, d.String())
	return []byte(b.String()), nil
}
