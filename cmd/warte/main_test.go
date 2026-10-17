package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"warte/internal/config"
)

// isolate points every warte path at temporary directories so tests never
// touch the user's config, data, logs or keychain entry.
func isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("WARTE_CONFIG", root+"/config/none.toml")
	t.Setenv("XDG_CONFIG_HOME", root+"/config")
	t.Setenv("XDG_DATA_HOME", root+"/data")
	t.Setenv("XDG_STATE_HOME", root+"/state")
	t.Setenv("XDG_CACHE_HOME", root+"/cache")
	t.Setenv(envPassphrase, "")
	t.Setenv(envKeyring, "mock")
	return root
}

// stubs records calls to the backend seams.
type stubs struct {
	unlinkErr error
	unlinked  int
	wiped     int
	exported  []string
	linked    bool
}

// stubBackend replaces every real-backend seam for the test.
func stubBackend(t *testing.T) *stubs {
	t.Helper()
	s := &stubs{}
	saved := []any{openRealEnv, newRealService, newRealLinker, realIsLinked, realUnlink, realWipe, realExport, closeEnv}
	openRealEnv = func(config.Config) error { return nil }
	realUnlink = func(context.Context, config.Config) error { s.unlinked++; return s.unlinkErr }
	realWipe = func(config.Config) (string, error) { s.wiped++; return "/data/warte", nil }
	realExport = func(_ context.Context, _ config.Config, p string) error {
		s.exported = append(s.exported, p)
		return nil
	}
	realIsLinked = func(context.Context, config.Config) (bool, error) { return s.linked, nil }
	closeEnv = func() {}
	t.Cleanup(func() {
		openRealEnv = saved[0].(func(config.Config) error)
		newRealService = saved[1].(func(context.Context, config.Config) (coreService, error))
		newRealLinker = saved[2].(func(context.Context, config.Config) (coreLinker, error))
		realIsLinked = saved[3].(func(context.Context, config.Config) (bool, error))
		realUnlink = saved[4].(func(context.Context, config.Config) error)
		realWipe = saved[5].(func(config.Config) (string, error))
		realExport = saved[6].(func(context.Context, config.Config, string) error)
		closeEnv = saved[7].(func())
	})
	return s
}

// withStdin feeds input to the prompts (not a terminal).
func withStdin(t *testing.T, input string) {
	t.Helper()
	oldIn, oldTTY := stdin, stdinIsTerminal
	stdin = strings.NewReader(input)
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdin, stdinIsTerminal = oldIn, oldTTY })
}

func TestVersionFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "warte ") {
		t.Fatalf("version output %q", out.String())
	}
}

func TestBadFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestArguments(t *testing.T) {
	isolate(t)
	s := stubBackend(t)
	withStdin(t, "")
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"unlink", "--demo"}, 0, "nothing to unlink"},
		{[]string{"unlink", "--demo", "--yes"}, 0, "nothing to unlink"},
		{[]string{"--yes"}, 2, "not defined"}, // --yes belongs to unlink
		{[]string{"export", "--yes", "x"}, 2, "not defined"},
		{[]string{"unlink"}, 1, "canceled"}, // EOF answers no
		{[]string{"export"}, 2, "usage"},
		{[]string{"export", "a", "b"}, 2, "usage"},
		{[]string{"export", "/tmp/x.warte"}, 0, "Backup written to /tmp/x.warte"},
		{[]string{"export", "--demo", "/tmp/x"}, 1, "demo"},
		{[]string{"bogus"}, 2, "unknown command"},
		{[]string{"run", "bogus"}, 2, "unknown command"},
		{[]string{"help"}, 0, "warte link"},
		{[]string{"help"}, 0, "WARTE_PASSPHRASE"},
		{[]string{"unlink", "-h"}, 0, "-yes"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		code := run(c.args, &out, &errb)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d (%s)", c.args, code, c.code, errb.String())
		}
		if !strings.Contains(out.String()+errb.String(), c.out) {
			t.Errorf("%v: output %q lacks %q", c.args, out.String()+errb.String(), c.out)
		}
	}
	if s.unlinked != 0 {
		t.Errorf("unlink ran without confirmation")
	}
	if len(s.exported) != 1 || s.exported[0] != "/tmp/x.warte" {
		t.Errorf("exported %v", s.exported)
	}
}

func TestExportMakesPathAbsolute(t *testing.T) {
	isolate(t)
	s := stubBackend(t)
	var out, errb bytes.Buffer
	if code := run([]string{"export", "rel.warte"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(s.exported) != 1 || !strings.HasPrefix(s.exported[0], "/") || !strings.HasSuffix(s.exported[0], "/rel.warte") {
		t.Fatalf("exported %v", s.exported)
	}
	if !strings.Contains(out.String(), "WARTEXP1") {
		t.Errorf("no format note: %q", out.String())
	}
}

func TestLinkRefusesWhenLinked(t *testing.T) {
	isolate(t)
	s := stubBackend(t)
	s.linked = true
	called := false
	newRealLinker = func(context.Context, config.Config) (coreLinker, error) { called = true; return nil, nil }
	var out, errb bytes.Buffer
	if code := run([]string{"link"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "already linked") || !strings.Contains(errb.String(), "warte unlink") {
		t.Fatalf("stderr %q", errb.String())
	}
	if called {
		t.Fatal("linker started although already linked")
	}
}
