// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
)

func buildInfo(version, modules string) []byte {
	head := make([]byte, goHeader)
	copy(head, goMagic)
	head[14], head[15] = 8, goFlagLine
	out := slices.Clone(head)
	for _, s := range []string{version, modules} {
		out = binary.AppendUvarint(out, uint64(len(s)))
		out = append(out, s...)
	}
	return out
}

// A program that reads build information holds the magic as data; its own build information comes later.
func TestGoStreamSkipsMagicAsData(t *testing.T) {
	var b bytes.Buffer
	b.Write(bytes.Repeat([]byte{0}, 3*goChunk-5))
	b.Write(goMagic)
	b.WriteString("\x08\x02rServerServiceEngineappBaseContextdocBaseapp-deploymenttargetsource")
	b.Write(bytes.Repeat([]byte{1}, goChunk))
	mods := "path\texample.com/app\nmod\texample.com/app\t(devel)\t\ndep\tgolang.org/x/net\tv0.17.0\th1:x=\n"
	b.Write(buildInfo("go1.22.3", mods))
	got := goStream(&b, false)
	want := []string{"pkg:golang/stdlib@1.22.3", "pkg:golang/golang.org/x/net@v0.17.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := goStream(bytes.NewReader(append(slices.Clone(goMagic), "\x08\x02garbage"...)), false); got != nil {
		t.Errorf("magic without a Go version read as %v", got)
	}
}

// In usr/bin, a vendor's Go executable is read; one the distribution built without modules is not.
func TestGoInSystemDirs(t *testing.T) {
	sys := Scanner{System: true}
	if !sys.IsExecutable("usr/bin/cloudflared", true) || sys.IsExecutable("usr/bin/cloudflared", false) || sys.IsExecutable("usr/lib/libfoo.so.1", true) ||
		sys.IsExecutable("etc/init.d/nginx", true) {
		t.Error("system executables")
	}
	vendor := buildInfo("go1.27.3", "path\tgithub.com/cloudflare/cloudflared/cmd/cloudflared\nmod\tgithub.com/cloudflare/cloudflared\t(devel)\t\ndep\tgithub.com/go-jose/go-jose/v4\tv4.1.4\th1:x=\n")
	distro := buildInfo("go1.19.8", "path\tgithub.com/containerd/containerd/cmd/ctr\n")
	sys.AddStream("usr/bin/cloudflared", bytes.NewReader(vendor))
	sys.AddStream("/usr/bin/ctr", bytes.NewReader(distro))
	if got := sys.Purls(); !slices.Equal(got, []string{"pkg:golang/github.com/go-jose/go-jose/v4@v4.1.4", "pkg:golang/stdlib@1.27.3"}) {
		t.Errorf("got %v", got)
	}
	var app Scanner // outside a system directory, or in a Lambda zip, every Go executable counts
	app.AddStream("bin/ctr", bytes.NewReader(distro))
	if got := app.Purls(); !slices.Equal(got, []string{"pkg:golang/stdlib@1.19.8"}) {
		t.Errorf("got %v", got)
	}
}

// A program's own module is kept at the version it was installed at, or, built from a clean git checkout, by
// its commit, which Resolve turns into the version tagged there.
func TestGoMainModule(t *testing.T) {
	const rev = "4b4eaa49b5c93460cac089ed6d87e1c68c29d8d1"
	clean := "path\tgithub.com/traefik/traefik/v3/cmd/traefik\nmod\tgithub.com/traefik/traefik/v3\t(devel)\t\n" +
		"build\tvcs=git\nbuild\tvcs.revision=" + rev + "\nbuild\tvcs.modified=false\n"
	installed := "path\texample.com/tool\nmod\texample.com/tool\tv1.2.3\th1:x=\n"
	modified := "path\texample.com/app\nmod\texample.com/app\t(devel)\t\nbuild\tvcs=git\nbuild\tvcs.revision=" + rev + "\nbuild\tvcs.modified=true\n"
	var asked []string
	s := Scanner{Resolve: func(mod, r string) string {
		asked = append(asked, mod+"@"+r)
		return "v3.1.0"
	}}
	for i, mods := range []string{clean, installed, modified} {
		s.AddStream(fmt.Sprintf("usr/local/bin/p%d", i), bytes.NewReader(buildInfo("go1.22.5", mods)))
	}
	want := []string{"pkg:golang/example.com/tool@v1.2.3", "pkg:golang/github.com/traefik/traefik/v3@v3.1.0", "pkg:golang/stdlib@1.22.5"}
	if got := s.Purls(); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !slices.Equal(asked, []string{"github.com/traefik/traefik/v3@" + rev}) {
		t.Errorf("asked %v", asked)
	}
	s.Resolve = func(string, string) string { return "" }
	if got := s.Purls(); slices.Contains(got, "pkg:golang/github.com/traefik/traefik/v3@v3.1.0") {
		t.Errorf("an unresolved commit listed: %v", got)
	}
}
