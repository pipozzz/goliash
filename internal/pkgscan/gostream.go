// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime/debug"
	"strings"
)

// A Go binary (Go 1.18 and later) keeps its build information inline after a 32-byte header that starts with
// goMagic: the Go version and the module list, each a varint length and its bytes. goStream finds it while the
// file streams by, holding a small window of it, not the whole executable (tens of megabytes each, on a host
// with little memory).

const (
	goHeader   = 32
	goMaxInfo  = 1 << 20 // the module list of the largest binaries is far smaller
	goChunk    = 64 << 10
	goFlagLine = 0x2 // inline strings, the format since Go 1.18
)

// goStream reads a Go executable's modules and Go version from r, as package URLs; nil when r is not one. A program
// that reads build information itself (a scanner, a debugger) holds the magic as data too: a match counts only when
// a Go version follows it.
func goStream(r io.Reader) []string {
	br := bufio.NewReaderSize(r, goChunk)
	window := make([]byte, 0, 2*goChunk)
	buf := make([]byte, goChunk)
	var done bool
	for {
		if !done {
			n, err := br.Read(buf)
			window = append(window, buf[:n]...)
			done = err != nil
		}
		from := 0
		for {
			i := bytes.Index(window[from:], goMagic)
			if i < 0 {
				break
			}
			i += from
			if len(window)-i < goPeek && !done {
				from = -1 // read more before judging it
				break
			}
			if goVersionAt(window[i:]) {
				return goInfo(io.MultiReader(bytes.NewReader(window[i:]), br))
			}
			from = i + 1
		}
		if from < 0 {
			continue
		}
		if done {
			return nil
		}
		// Keep the end that may hold the first bytes of the magic.
		if keep := len(goMagic) - 1; len(window) > keep {
			window = append(window[:0], window[len(window)-keep:]...)
		}
	}
}

// goPeek is enough of the build information to see its Go version.
const goPeek = goHeader + 1 + 64

// goVersionAt reports whether b starts with build information whose Go version reads like one.
func goVersionAt(b []byte) bool {
	if len(b) < goHeader+1 || b[15]&goFlagLine == 0 {
		return false
	}
	n, k := binary.Uvarint(b[goHeader:])
	if k <= 0 || n < 3 || n > 64 || len(b) < goHeader+k+int(n) {
		return false
	}
	v := string(b[goHeader+k : goHeader+k+int(n)])
	return strings.HasPrefix(v, "go1.") || strings.HasPrefix(v, "devel ")
}

// goInfo reads the build information that follows the magic.
func goInfo(r io.Reader) []string {
	br := bufio.NewReader(r)
	head := make([]byte, goHeader)
	if _, err := io.ReadFull(br, head); err != nil || head[15]&goFlagLine == 0 {
		return nil // an older, pointer-based layout: not read
	}
	vers, err := goString(br)
	if err != nil {
		return nil
	}
	mod, err := goString(br)
	if err != nil {
		return nil
	}
	if len(mod) >= 33 && mod[len(mod)-17] == '\n' { // the module list sits between two 16-byte markers
		mod = mod[16 : len(mod)-16]
	}
	info, err := debug.ParseBuildInfo(mod)
	if err != nil {
		info = &debug.BuildInfo{}
	}
	var out []string
	if v := strings.TrimPrefix(vers, "go"); v != "" {
		out = append(out, "pkg:golang/stdlib@"+strings.SplitN(v, " ", 2)[0])
	}
	for _, d := range info.Deps {
		m := d
		if d.Replace != nil {
			m = d.Replace
		}
		if m.Version != "" && m.Version != "(devel)" {
			out = append(out, "pkg:golang/"+m.Path+"@"+m.Version)
		}
	}
	return out
}

func goString(br *bufio.Reader) (string, error) {
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return "", err
	}
	if n > goMaxInfo {
		return "", errors.New("too long")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// IsExecutable reports whether Wants takes a file for the build information of a Go executable: then AddStream
// reads it as it streams by.
func (s *Scanner) IsExecutable(name string, executable bool) bool {
	return executable && !strings.Contains(baseName(name), ".") && s.Wants(name, 0, true) > 0
}

// AddStream reads an executable's Go build information while it streams by.
func (s *Scanner) AddStream(r io.Reader) {
	if s.found == nil {
		s.found = map[string]bool{}
	}
	for _, p := range goStream(r) {
		s.found[p] = true
	}
}
