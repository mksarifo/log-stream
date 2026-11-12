.PHONY: setup build build-windows run dev test

WINDOWS_CC ?= x86_64-w64-mingw32-gcc

setup:
	go mod download
	cd web && npm ci

build:
	cd web && npm run build
	go build -o bin/logstream ./cmd/logstream

# SQLite uses CGO, so cross-compilation needs a Windows C compiler.
build-windows:
	cd web && npm run build
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC="$(WINDOWS_CC)" go build -trimpath -ldflags='-s -w -linkmode external -extldflags=-static' -o bin/logstream-windows-amd64.exe ./cmd/logstream
	python3 scripts/package_windows.py

run: build
	./bin/logstream

dev:
	go run ./cmd/logstream

test:
	go test -race ./...
	cd web && npm test
	cd web && npm run build
