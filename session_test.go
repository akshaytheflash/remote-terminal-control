package main

import (
	"strings"
	"testing"
)

func TestHistorySmall(t *testing.T) {
	h := newHistory(10)
	h.Write([]byte("abc"))
	if got := string(h.Snapshot()); got != "abc" {
		t.Fatalf("got %q", got)
	}
	h.Write([]byte("defghij"))
	if got := string(h.Snapshot()); got != "abcdefghij" {
		t.Fatalf("got %q", got)
	}
	h.Write([]byte("KLM"))
	if got := string(h.Snapshot()); got != "defghijKLM" {
		t.Fatalf("got %q", got)
	}
}

func TestHistoryOverflow(t *testing.T) {
	h := newHistory(10)
	h.Write([]byte("0123456789"))
	if got := string(h.Snapshot()); got != "0123456789" {
		t.Fatalf("got %q", got)
	}
	h.Write([]byte("abc"))
	if got := string(h.Snapshot()); got != "3456789abc" {
		t.Fatalf("got %q", got)
	}
	h.Write([]byte("0123456789XY"))
	if got := string(h.Snapshot()); got != "23456789XY" {
		t.Fatalf("got %q", got)
	}
}

func TestHistoryExactWrap(t *testing.T) {
	h := newHistory(8)
	h.Write([]byte("aaaabbbb"))
	h.Write([]byte("cccc"))
	if got := string(h.Snapshot()); got != "bbbbcccc" {
		t.Fatalf("got %q", got)
	}
}

func TestAuthSingleUse(t *testing.T) {
	a := NewAuth()
	pw := a.Password()
	if len(pw) != 9 {
		t.Fatalf("unexpected password format %q", pw)
	}

	// case and separators should not matter
	tok, err := a.Login(strings.ToLower(strings.ReplaceAll(pw, "-", " ")))
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if !a.Valid(tok) {
		t.Fatal("token should be valid")
	}
	if a.Password() != "" {
		t.Fatal("password should be burned")
	}
	if _, err := a.Login(pw); err == nil {
		t.Fatal("password should not work twice")
	}
}

func TestAuthLockout(t *testing.T) {
	a := NewAuth()
	for i := 0; i < 3; i++ {
		if _, err := a.Login("WRONG-CODE"); err == nil {
			t.Fatal("expected failure")
		}
	}
	if _, err := a.Login(a.Password()); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected lockout, got %v", err)
	}
	// rotating clears the lockout
	a.Rotate()
	if _, err := a.Login(a.Password()); err != nil {
		t.Fatalf("login after rotate failed: %v", err)
	}
}

func TestTrimToBoundary(t *testing.T) {
	in := []byte("garbage\x1b[31mred")
	out := string(trimToBoundary(in))
	if out != "\x1b[31mred" {
		t.Fatalf("got %q", out)
	}
}
