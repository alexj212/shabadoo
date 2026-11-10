// Package machoplist embeds an Info.plist into a Mach-O executable that Go has
// already linked, as the `__TEXT,__info_plist` section.
//
// macOS reads that section to identify a standalone executable that is not
// inside an app bundle. Local Network privacy needs it: launchd agents are not
// exempt from it (Apple TN3179), and the sessions shabadoo launches are
// attributed to shabadoo as their responsible code. Without an embedded plist,
// non-Apple tools in those sessions (adb, Homebrew curl) were refused LAN
// access with no prompt (reported from the mac, 2026-11-10).
//
// The usual way is `-linkmode external -extldflags '-sectcreate __TEXT
// __info_plist Info.plist'`, which needs a darwin linker. The darwin builds are
// cross-compiled on linux by Go's internal linker, which cannot add a section,
// so this adds it after linking:
//
//   - the section header goes into the load commands, and the plist bytes into
//     the free space Go's linker leaves between the load commands and the first
//     section. Both lie inside __TEXT, so no segment moves and the file size
//     does not change;
//   - the linker's ad hoc code signature is repaired by rehashing its pages.
//     An arm64 Mac refuses to run a binary whose signature does not match, and
//     `upgrade` runs the new binary before it re-signs it.
//
// Stdlib only, and it refuses anything it does not recognise rather than
// guessing.
package machoplist

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	magic64        = 0xfeedfacf
	headerSize64   = 32
	lcSegment64    = 0x19
	lcCodeSig      = 0x1d
	segmentCmdSize = 72
	sectionSize64  = 80

	csMagicEmbedded = 0xfade0cc0
	csMagicCodeDir  = 0xfade0c02
	csSlotCodeDir   = 0
	csHashSHA256    = 2
)

var le = binary.LittleEndian
var be = binary.BigEndian

// Embed returns a copy of bin carrying plist as __TEXT,__info_plist, with any
// ad hoc code signature rehashed to match.
func Embed(bin, plist []byte) ([]byte, error) {
	if len(plist) == 0 {
		return nil, errors.New("empty plist")
	}
	if len(bin) < headerSize64 || le.Uint32(bin) != magic64 {
		return nil, errors.New("not a 64-bit little-endian Mach-O")
	}
	out := bytes.Clone(bin)
	ncmds := le.Uint32(out[16:])
	sizeofcmds := le.Uint32(out[20:])
	cmdsEnd := headerSize64 + int(sizeofcmds)
	if cmdsEnd > len(out) {
		return nil, errors.New("load commands run past the end of the file")
	}

	// Find __TEXT, the first section's file offset, and any existing plist.
	textAt := -1
	var textAddr uint64
	firstData := len(out)
	off := headerSize64
	for i := uint32(0); i < ncmds; i++ {
		if off+8 > cmdsEnd {
			return nil, errors.New("truncated load command")
		}
		cmd, size := le.Uint32(out[off:]), int(le.Uint32(out[off+4:]))
		if size < 8 || off+size > cmdsEnd {
			return nil, errors.New("malformed load command size")
		}
		if cmd == lcSegment64 {
			name := cstr(out[off+8 : off+24])
			nsects := int(le.Uint32(out[off+64:]))
			if segmentCmdSize+nsects*sectionSize64 > size {
				return nil, fmt.Errorf("segment %s: sections overrun the command", name)
			}
			if name == "__TEXT" {
				textAt, textAddr = off, le.Uint64(out[off+24:])
			}
			for s := 0; s < nsects; s++ {
				sec := out[off+segmentCmdSize+s*sectionSize64:]
				if name == "__TEXT" && cstr(sec[0:16]) == "__info_plist" {
					return nil, errors.New("already has __TEXT,__info_plist")
				}
				if fo := int(le.Uint32(sec[48:])); fo != 0 && fo < firstData {
					firstData = fo
				}
			}
		}
		off += size
	}
	if textAt < 0 {
		return nil, errors.New("no __TEXT segment")
	}
	if le.Uint64(out[textAt+40:]) != 0 {
		return nil, errors.New("__TEXT does not start at file offset 0; header space is not inside it")
	}

	// Room: the new section header, then the plist, both before the first
	// section's bytes. The space must currently be zero, or it is not free.
	newEnd := cmdsEnd + sectionSize64
	dataOff := (newEnd + 15) &^ 15
	if dataOff+len(plist) > firstData {
		return nil, fmt.Errorf("no room: plist needs %d bytes, %d free after the load commands",
			len(plist)+sectionSize64+(dataOff-newEnd), firstData-cmdsEnd)
	}
	for _, c := range out[cmdsEnd : dataOff+len(plist)] {
		if c != 0 {
			return nil, errors.New("header padding is not empty")
		}
	}

	// Insert the section header as __TEXT's first section: it has the lowest
	// address in the segment, and sections are listed in address order.
	sect := make([]byte, sectionSize64)
	copy(sect[0:16], "__info_plist")
	copy(sect[16:32], "__TEXT")
	le.PutUint64(sect[32:], textAddr+uint64(dataOff)) // addr
	le.PutUint64(sect[40:], uint64(len(plist)))       // size
	le.PutUint32(sect[48:], uint32(dataOff))          // offset
	// align 2^0, no relocations, S_REGULAR: what ld64's -sectcreate produces.

	at := textAt + segmentCmdSize
	copy(out[at+sectionSize64:newEnd], out[at:cmdsEnd])
	copy(out[at:], sect)
	le.PutUint32(out[textAt+4:], le.Uint32(out[textAt+4:])+sectionSize64)
	le.PutUint32(out[textAt+64:], le.Uint32(out[textAt+64:])+1)
	le.PutUint32(out[20:], sizeofcmds+sectionSize64)
	copy(out[dataOff:], plist)

	if err := rehash(out); err != nil {
		return nil, err
	}
	return out, nil
}

// rehash recomputes the page hashes of an ad hoc code signature, if there is
// one. Only the code slots are touched: they are what the edit invalidated.
// A signature with special slots or a CMS blob is a real signature, which this
// cannot reproduce, so it is refused rather than left broken.
func rehash(b []byte) error {
	cd, err := codeDirectory(b)
	if err != nil || cd == nil {
		return err
	}
	hashOff := int(be.Uint32(cd[16:]))
	nCode := int(be.Uint32(cd[28:]))
	codeLimit := int(be.Uint32(cd[32:]))
	pageSize := 1 << cd[39]
	for i := 0; i < nCode; i++ {
		start := i * pageSize
		end := min(start+pageSize, codeLimit)
		sum := sha256.Sum256(b[start:end])
		copy(cd[hashOff+i*sha256.Size:], sum[:])
	}
	return nil
}

// codeDirectory returns the ad hoc CodeDirectory inside b, aliasing b, or nil
// when the binary is unsigned.
func codeDirectory(b []byte) ([]byte, error) {
	ncmds := le.Uint32(b[16:])
	off := headerSize64
	for i := uint32(0); i < ncmds; i++ {
		cmd, size := le.Uint32(b[off:]), int(le.Uint32(b[off+4:]))
		if cmd == lcCodeSig {
			so, ss := int(le.Uint32(b[off+8:])), int(le.Uint32(b[off+12:]))
			if so+ss > len(b) || ss < 12 {
				return nil, errors.New("code signature runs past the end of the file")
			}
			sb := b[so : so+ss]
			if be.Uint32(sb) != csMagicEmbedded {
				return nil, errors.New("unrecognised code signature")
			}
			count := int(be.Uint32(sb[8:]))
			if count != 1 || be.Uint32(sb[12:]) != csSlotCodeDir {
				return nil, errors.New("signature has more than a CodeDirectory; not an ad hoc linker signature")
			}
			cd := sb[be.Uint32(sb[16:]):]
			switch {
			case be.Uint32(cd) != csMagicCodeDir:
				return nil, errors.New("unrecognised CodeDirectory")
			case be.Uint32(cd[24:]) != 0:
				return nil, errors.New("CodeDirectory has special slots; not an ad hoc linker signature")
			case cd[36] != sha256.Size || cd[37] != csHashSHA256:
				return nil, errors.New("CodeDirectory is not SHA-256")
			case int(be.Uint32(cd[32:])) != so:
				return nil, errors.New("CodeDirectory does not cover up to the signature")
			}
			return cd, nil
		}
		off += size
	}
	return nil, nil
}

// Verify reports whether an ad hoc signature's page hashes match the file.
// An unsigned binary verifies trivially. Used by the tests, and by the build
// to check its own output.
func Verify(b []byte) error {
	cd, err := codeDirectory(b)
	if err != nil || cd == nil {
		return err
	}
	hashOff := int(be.Uint32(cd[16:]))
	nCode := int(be.Uint32(cd[28:]))
	codeLimit := int(be.Uint32(cd[32:]))
	pageSize := 1 << cd[39]
	if want := (codeLimit + pageSize - 1) / pageSize; nCode != want {
		return fmt.Errorf("CodeDirectory has %d page slots, file needs %d", nCode, want)
	}
	for i := 0; i < nCode; i++ {
		start := i * pageSize
		end := min(start+pageSize, codeLimit)
		sum := sha256.Sum256(b[start:end])
		if !bytes.Equal(cd[hashOff+i*sha256.Size:hashOff+(i+1)*sha256.Size], sum[:]) {
			return fmt.Errorf("page %d hash does not match", i)
		}
	}
	return nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
