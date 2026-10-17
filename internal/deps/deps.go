//go:build tools

// Package deps keeps go.mod requirements that no real package imports yet, so
// "go mod tidy" does not drop them. It is never compiled into warte (build tag
// "tools"). Remove an entry once a real import exists, or drop the module from
// go.mod deliberately.
//
//   - glamour: reserved for Markdown rendering; not used by any package yet.
//     Tidy would remove it from go.mod.
//   - go-sqlite3: only a transitive dependency of signalmeow. Tidy would keep it
//     but mark it "// indirect". Never import it in real code: it clashes with
//     go-sqlcipher's bundled SQLite (see internal/store/doc.go).
package deps

import (
	_ "charm.land/glamour/v2"
	_ "github.com/mattn/go-sqlite3"
)
