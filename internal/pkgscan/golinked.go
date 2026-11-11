// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"bytes"
	"debug/elf"
	"debug/gosym"
	"debug/macho"
	"strings"
)

// goLinked reads which Go packages an executable links, from the functions in its line table (.gopclntab, there in
// stripped executables too): a Go advisory about net/http does not concern a program without net/http. Packages,
// not functions: the compiler inlines small functions into their callers, where the line table no longer names
// them, but a package rarely disappears whole.
func goLinked(b []byte) []string {
	pclntab, text := pclnTable(b)
	if len(pclntab) == 0 {
		return nil
	}
	tab, err := gosym.NewTable(nil, gosym.NewLineTable(pclntab, text))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range tab.Funcs {
		if p := PackageOf(f.Name); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// PackageOf is the package path of a function's name in a Go executable: "net/http.(*Server).Serve" is in
// net/http, "golang.org/x/net/http2.(*Framer).ReadFrame" in golang.org/x/net/http2, "type:.eq.x" in none.
func PackageOf(name string) string {
	if i := strings.Index(name, "[...]"); i >= 0 {
		name = name[:i]
	}
	slash := strings.LastIndex(name, "/")
	dot := strings.Index(name[slash+1:], ".")
	if dot <= 0 {
		return ""
	}
	p := name[:slash+1+dot]
	if strings.ContainsAny(p, ":( ") {
		return ""
	}
	return p
}

// pclnTable finds an executable's Go line table and where its code starts, for ELF and Mach-O. Windows (PE)
// executables keep it where only their symbol table, gone when stripped, says: they are not read.
func pclnTable(b []byte) ([]byte, uint64) {
	r := bytes.NewReader(b)
	if f, err := elf.NewFile(r); err == nil {
		var text uint64
		if s := f.Section(".text"); s != nil {
			text = s.Addr
		}
		if s := f.Section(".gopclntab"); s != nil {
			if d, err := s.Data(); err == nil {
				return d, text
			}
		}
		return nil, 0
	}
	if f, err := macho.NewFile(r); err == nil {
		var text uint64
		if s := f.Section("__text"); s != nil {
			text = s.Addr
		}
		if s := f.Section("__gopclntab"); s != nil {
			if d, err := s.Data(); err == nil {
				return d, text
			}
		}
	}
	return nil, 0
}
