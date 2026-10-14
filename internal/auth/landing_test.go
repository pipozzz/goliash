// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import "testing"

func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/oauth/authorize?client_id=x": true, "/": true, "/services/web": true,
		"//evil.example/": false, "/\\evil.example": false, "https://evil.example": false, "evil": false,
		"/x\r\nSet-Cookie: a=b": false, "": false,
	} {
		if localPath(p) != want {
			t.Errorf("localPath(%q) = %v", p, !want)
		}
	}
}
