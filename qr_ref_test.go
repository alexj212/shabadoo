package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestQRMatchesReferenceEncoder is the only test that can honestly say this
// encoder works: a hand-written QR that looks plausible and does not scan is
// the expected failure, so it is compared module-for-module against qrencode
// (libqrencode), an independent implementation.
//
// It compares mask by mask. All eight masks produce VALID symbols and the
// standard only says to pick the lowest-penalty one, so two correct encoders
// can legitimately disagree on which to emit. Matching ANY mask exactly means
// the data encoding, Reed-Solomon, interleaving, module placement and format
// bits are all right — everything except a cosmetic choice.
func TestQRMatchesReferenceEncoder(t *testing.T) {
	if _, err := exec.LookPath("qrencode"); err != nil {
		t.Skip("qrencode not installed — cannot verify against a reference")
	}
	for _, payload := range []string{
		"https://coordinator.example/pair#code=A1B2C3D4",
		"https://x.io/pair#code=00000000",
		// The shapes `shabadoo pair --qr` actually emits. A label pushes the
		// payload past 60 bytes and into a higher version, which is where a
		// hand-written encoder gets its capacity arithmetic wrong — and the
		// failure is a code that renders beautifully and scans as nothing.
		"https://coordinator.example/pair#code=8144D9CF&label=Alex%27s%20iPhone%2015",
		"https://coordinator.example/pair#code=F02F9BB1&label=qr-repro-test",
		// A long coordinator host with a long label: the realistic worst case.
		"https://coordinator.internal.example.com/pair#code=DEADBEEF" +
			"&label=Someone%27s%20Very%20Long%20iPhone%20Name%20Here",
		"HELLO",
	} {
		t.Run(payload, func(t *testing.T) {
			ref := referenceMatrix(t, payload)

			v, data, err := encodeData([]byte(payload))
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			size := v.version*4 + 17
			if len(ref) != size {
				t.Fatalf("chose version %d (%dx%d), reference is %dx%d — capacity tables are wrong",
					v.version, size, size, len(ref), len(ref))
			}

			for mask := range 8 {
				if sameMatrix(ref, gridOf(encodeWithMask(v, data, mask))) {
					return // exact match on this mask: everything checks out
				}
			}
			t.Errorf("no mask reproduced the reference symbol — this QR will not scan")
		})
	}
}

func referenceMatrix(t *testing.T, payload string) [][]bool {
	t.Helper()
	png := t.TempDir() + "/ref.png"
	// -8: byte mode, matching this encoder — qrencode would otherwise pick
	// alphanumeric for an uppercase payload and emit a different symbol.
	// -s 1: one pixel per module. -m 0: no quiet zone, so indices line up.
	if out, err := exec.Command("qrencode", "-8", "-l", "M", "-s", "1", "-m", "0",
		"-o", png, payload).CombinedOutput(); err != nil {
		t.Fatalf("qrencode: %v: %s", err, out)
	}
	// Decode the PNG without an image dependency: ask Go's image/png.
	f, err := os.Open(png)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return decodePNG(t, f)
}

func sameMatrix(a, b [][]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for y := range a {
		if len(a[y]) != len(b[y]) {
			return false
		}
		for x := range a[y] {
			if a[y][x] != b[y][x] {
				return false
			}
		}
	}
	return true
}

func init() { _ = fmt.Sprint }

// RenderSVG is only a drawing layer over a matrix that is already verified
// against libqrencode — but a drawing layer that drops or shifts modules
// produces a symbol that looks like a QR and scans as nothing. This checks the
// SVG carries exactly the dark modules Encode produced, at the right offsets.
func TestRenderSVGMatchesMatrix(t *testing.T) {
	const payload = "https://coordinator.example/pair#code=A1B2C3D4&label=Dev%26Ops"
	grid, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	svg, err := RenderSVG(payload)
	if err != nil {
		t.Fatal(err)
	}

	// Every dark module is one "M<x> <y>h1v1h-1z" subpath, offset by the quiet
	// zone. Rebuild the set from the SVG and compare.
	const quiet = 4
	re := regexp.MustCompile(`M(\d+) (\d+)h1v1h-1z`)
	got := map[[2]int]bool{}
	for _, m := range re.FindAllStringSubmatch(string(svg), -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		got[[2]int{x - quiet, y - quiet}] = true
	}

	want := 0
	for y, row := range grid {
		for x, dark := range row {
			if !dark {
				continue
			}
			want++
			if !got[[2]int{x, y}] {
				t.Fatalf("module (%d,%d) is dark in the matrix but missing from the SVG", x, y)
			}
		}
	}
	if len(got) != want {
		t.Errorf("SVG has %d dark modules, matrix has %d", len(got), want)
	}

	// The quiet zone is not decoration: scanners rely on it, and a symbol drawn
	// flush to the edge of a dark page is unreadable.
	size := len(grid) + quiet*2
	if !strings.Contains(string(svg), fmt.Sprintf(`viewBox="0 0 %d %d"`, size, size)) {
		t.Errorf("viewBox does not include the 4-module quiet zone")
	}
	if !strings.Contains(string(svg), `fill="#fff"`) {
		t.Error("no white background — a QR on a dark page will not scan")
	}
}

// Every cell must set BOTH colours.
//
// The renderer originally set only a background and let `▄` paint its lower
// half in the terminal's default foreground — so on a dark theme the
// light-over-dark cells inverted, and on a light theme the dark-over-light ones
// did. Roughly a quarter of the modules were wrong, and the result still looked
// convincingly like a QR code. A camera is the only thing that noticed.
func TestRenderTerminalSetsBothColours(t *testing.T) {
	// A grid with all four cell cases: dark/dark, dark/light, light/dark,
	// light/light appear in the first two rows.
	grid := [][]bool{
		{true, true, false, false, true},
		{true, false, true, false, false},
		{false, false, false, false, false},
		{false, false, false, false, false},
		{false, false, false, false, false},
	}
	out := RenderTerminal(grid)

	// Split into the escape+char runs the renderer emits.
	for _, run := range strings.Split(out, "\033[0m") {
		i := strings.Index(run, "\033[")
		if i < 0 {
			continue
		}
		seq := run[i:]
		end := strings.Index(seq, "m")
		if end < 0 {
			continue
		}
		codes, body := seq[2:end], seq[end+1:]
		if !strings.Contains(body, "▄") {
			continue // a plain space takes its colour from the background alone
		}
		// A half-block cell shows foreground AND background, so both must be
		// stated. Relying on the terminal's default for either is the bug.
		hasFG := strings.Contains(codes, "30") || strings.Contains(codes, "37")
		hasBG := strings.Contains(codes, "40") || strings.Contains(codes, "47")
		if !hasFG || !hasBG {
			t.Errorf("half-block cell with codes %q sets fg=%v bg=%v — both are required",
				codes, hasFG, hasBG)
		}
	}
}

// The half-block encoding must survive the odd final row. QR symbols are always
// an odd number of modules and the quiet zone keeps them odd, so this is not an
// edge case — it is every symbol.
func TestRenderTerminalOddHeight(t *testing.T) {
	for _, size := range []int{21, 25, 29, 45} {
		grid := make([][]bool, size)
		for i := range grid {
			grid[i] = make([]bool, size)
			grid[i][i] = true // a diagonal, so a dropped row is visible
		}
		out := RenderTerminal(grid)
		rows := strings.Count(out, "\n")
		full := size + 8
		want := (full + 1) / 2
		if rows != want {
			t.Errorf("size %d: %d terminal rows, want %d", size, rows, want)
		}
	}
}
