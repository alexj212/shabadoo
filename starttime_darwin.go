//go:build darwin

package main

// Exact process start time, as a stable token. See starttime_linux.go for why
// this is a key rather than a clock.
//
// There is no /proc here. kinfo_proc begins with struct extern_proc, whose
// first union member is struct timeval __p_starttime, so the value is the first
// 16 bytes of kern.proc.pid.<pid> — the counterpart of /proc/<pid>/stat field
// 22: an exact integer, with no shell, no parsing, no locale and no timezone in
// the path.
//
// Measured on darwin by the node that runs it, which is the only place these
// facts can be established:
//
//   - `ps -o start=` DISCARDS the minute and second for an older process
//     ("Wed01PM" for one 36 hours old), so it collides with anything started in
//     that hour on any Wednesday.
//   - `ps -o lstart=` is full precision but locale AND timezone formatted: the
//     same process renders four different strings under de_DE, ja_JP, TZ=UTC
//     and TZ=Asia/Tokyo. An MCP child inherits its environment from whatever
//     launched it while the agent runs under its own, so two readers would
//     disagree permanently and every child would sit at "unknown" forever —
//     which looks exactly like a detector that is working and finding nothing.
//
// Do NOT reach for syscall.Sysctl: it returns a string and truncates at the
// first null byte, which for binary kinfo_proc is immediately, so it yields an
// empty result rather than an error. syscall.SysctlRaw is not exported on
// darwin, which is why this uses x/sys/unix — already in every build indirectly
// through modernc.org/sqlite, so it costs no new module.

import (
	"encoding/binary"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

func processStartToken(pid int) (string, error) {
	buf, err := unix.SysctlRaw("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("%w: sysctl kern.proc.pid.%d: %v", ErrIndeterminate, pid, err)
	}
	// struct timeval on 64-bit darwin: int64 seconds, int32 microseconds.
	if len(buf) < 16 {
		return "", fmt.Errorf("%w: short kinfo_proc for %d: %d bytes",
			ErrIndeterminate, pid, len(buf))
	}
	sec := int64(binary.LittleEndian.Uint64(buf[0:8]))
	usec := int64(binary.LittleEndian.Uint32(buf[8:12]))

	// Plausibility, deliberately loose. The layout is stable ABI, but if it ever
	// moves these bytes become a number wrong by decades rather than by seconds
	// — that is all this needs to catch. A TIGHT bound here manufactures false
	// unknowns, which is the same under-reporting the whole mechanism exists to
	// avoid.
	start := time.Unix(sec, usec*1000)
	if start.Year() < 2000 || start.After(time.Now().Add(5*time.Minute)) {
		return "", fmt.Errorf("%w: implausible start %s for %d — kinfo_proc layout may have moved",
			ErrIndeterminate, start, pid)
	}
	return fmt.Sprintf("%d.%06d", sec, usec), nil
}
