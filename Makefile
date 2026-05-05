.PHONY: build test vet clean

# libghostty-vt is a cgo + pkg-config dependency. The mitchellh/go-libghostty
# repo builds it via CMake into build/_deps/ghostty-src/zig-out/share/pkgconfig.
LIBGHOSTTY ?= $(HOME)/github.com/mitchellh/go-libghostty
PKG_CONFIG_PATH := $(LIBGHOSTTY)/build/_deps/ghostty-src/zig-out/share/pkgconfig
export PKG_CONFIG_PATH

build:
	go build -o bin/hoot ./cmd/hoot

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin
