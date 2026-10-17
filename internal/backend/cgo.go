package backend

// Link flags for libsignal-ffi (lib/libsignal_ffi.a, built by `make libsignal`).
// Keeping them here means plain `go build`/`go test` work without CGO_LDFLAGS.

/*
#cgo LDFLAGS: -L${SRCDIR}/../../lib
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation -framework SystemConfiguration
*/
import "C"
