package keymap

import "testing"

func TestIsValidRegister(t *testing.T) {
	valid := `abcxyzABCZ0189"_+*-`
	for _, r := range valid {
		if !IsValidRegister(r) {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range "!@#. \x00é" {
		if IsValidRegister(r) {
			t.Errorf("%q should be invalid", r)
		}
	}
	if !IsClipboard('+') || !IsClipboard('*') || IsClipboard('a') {
		t.Error("IsClipboard")
	}
}

func TestRegisterStore(t *testing.T) {
	var r Registers
	if _, ok := r.Get(0); ok {
		t.Fatal("empty registers should miss")
	}
	steps := []struct {
		reg  rune
		text string
	}{
		{0, "one"},
		{'a', "two"},
		{'A', "three"},
		{'_', "gone"},
		{'+', "clip"},
		{'!', "ignored"},
	}
	for _, s := range steps {
		r.Yank(s.reg, s.text)
	}
	tests := []struct {
		reg  rune
		want string
		ok   bool
	}{
		{0, "clip", true},
		{'"', "clip", true},
		{'0', "one", true},
		{'a', "two\nthree", true},
		{'A', "two\nthree", true},
		{'+', "clip", true},
		{'_', "", false},
		{'b', "", false},
		{'!', "", false},
	}
	for _, tt := range tests {
		got, ok := r.Get(tt.reg)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Get(%q) = %q %v, want %q %v", tt.reg, got, ok, tt.want, tt.ok)
		}
	}
	r.Yank('B', "fresh")
	if got, _ := r.Get('b'); got != "fresh" {
		t.Errorf("append to empty register = %q", got)
	}
	r.Clear()
	if _, ok := r.Get(0); ok {
		t.Fatal("Clear")
	}
}
