# warte build. libsignal (Rust) is compiled once into lib/libsignal_ffi.a at the
# exact commit pinned by the mautrix-signal version in go.mod.

LIBDIR      := $(CURDIR)/lib
THIRD_PARTY := $(CURDIR)/third_party
MS_VERSION  := $(shell go list -m -f '{{.Version}}' go.mau.fi/mautrix-signal)
MS_COMMIT   := $(lastword $(subst -, ,$(MS_VERSION)))

export CGO_ENABLED := 1
export GOFLAGS     := -tags=sqlite_fts5 $(GOFLAGS)
export CGO_LDFLAGS := -L$(LIBDIR) $(if $(filter Darwin,$(shell uname)),-framework Security -framework CoreFoundation -framework SystemConfiguration) $(CGO_LDFLAGS)

# install location ("make install PREFIX=~/.local") and version stamp.
PREFIX  ?= /usr/local
BINDIR  ?= $(PREFIX)/bin
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build run demo install uninstall test vet lint libsignal clean

all: build

# Every build links the real backend, so it needs lib/libsignal_ffi.a.
build: $(LIBDIR)/libsignal_ffi.a
	go build -ldflags "$(LDFLAGS)" -o warte ./cmd/warte

run: build
	./warte

demo: build
	./warte --demo

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 warte $(DESTDIR)$(BINDIR)/warte

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/warte

test: $(LIBDIR)/libsignal_ffi.a
	go test ./...

vet: $(LIBDIR)/libsignal_ffi.a
	go vet ./...

libsignal: $(LIBDIR)/libsignal_ffi.a

$(LIBDIR)/libsignal_ffi.a:
	@echo "building libsignal-ffi for mautrix-signal $(MS_COMMIT)"
	@mkdir -p $(THIRD_PARTY) $(LIBDIR)
	@if [ ! -d $(THIRD_PARTY)/mautrix-signal ]; then \
		git clone -q https://github.com/mautrix/signal.git $(THIRD_PARTY)/mautrix-signal; \
	fi
	cd $(THIRD_PARTY)/mautrix-signal && git fetch -q origin && git checkout -q $(MS_COMMIT) && \
		git submodule update --init --depth 1 pkg/libsignalgo/libsignal
	cd $(THIRD_PARTY)/mautrix-signal/pkg/libsignalgo/libsignal && \
		RUSTFLAGS="-Ctarget-feature=-crt-static" RUSTC_WRAPPER="" cargo build -p libsignal-ffi --profile=release
	cp $(THIRD_PARTY)/mautrix-signal/pkg/libsignalgo/libsignal/target/release/libsignal_ffi.a $(LIBDIR)/

clean:
	rm -f warte
