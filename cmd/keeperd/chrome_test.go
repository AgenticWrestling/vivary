package main

import "testing"

func TestChromeDebugPort(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:9222", "9222"},
		{"0.0.0.0:9223", "9223"},
		{"localhost:9222", "9222"},
		{"[::1]:9222", "9222"},   // IPv6 bracketed address
		{"host:443", "443"},
		{"9222", "9222"},         // no colon → fallback matches the only "word"
		{"", "9222"},             // empty → fallback
		{"noport", "9222"},       // no colon → fallback
	}
	for _, tc := range cases {
		got := chromeDebugPort(tc.addr)
		if got != tc.want {
			t.Errorf("chromeDebugPort(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
