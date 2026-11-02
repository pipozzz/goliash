// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"bytes"
	"encoding/binary"
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
	got := goStream(&b)
	want := []string{"pkg:golang/stdlib@1.22.3", "pkg:golang/golang.org/x/net@v0.17.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := goStream(bytes.NewReader(append(slices.Clone(goMagic), "\x08\x02garbage"...))); got != nil {
		t.Errorf("magic without a Go version read as %v", got)
	}
}
