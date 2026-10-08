test:
	go test ./...

# The sherpa-onnx binding links two dylibs and gives the binary an rpath into
# the Go module cache. `make build` copies the dylibs next to bin/journal,
# adds an @executable_path rpath, removes the module-cache rpath and re-signs
# (with the "WorkJournal Local Signing" certificate when present, else ad hoc; a
# stable identity keeps macOS privacy grants such as Desktop-folder access across
# rebuilds), so bin/journal loads its own copies and survives
# `go clean -modcache`. (Plain `go build` also works, but that binary loads
# the dylibs from the module cache.)
SIGN_IDENTITY ?= WorkJournal Local Signing
SHERPA_ARCH := $(if $(filter arm64,$(shell uname -m)),aarch64-apple-darwin,x86_64-apple-darwin)
SHERPA_LIB = $(shell go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-macos)/lib/$(SHERPA_ARCH)

build:
	mkdir -p bin
	cp -f "$(SHERPA_LIB)/libsherpa-onnx-c-api.dylib" "$(SHERPA_LIB)/libonnxruntime.dylib" bin/
	rm -f bin/journal
	go build -ldflags='-extldflags "-Wl,-rpath,@executable_path"' -o bin/journal ./cmd/journal
	install_name_tool -delete_rpath "$(SHERPA_LIB)" bin/journal
	if security find-identity -p codesigning 2>/dev/null | grep -qF "\"$(SIGN_IDENTITY)\""; then \
		codesign -f -s "$(SIGN_IDENTITY)" bin/journal; \
	else codesign -f -s - bin/journal; fi

recorder:
	./recorder/build.sh

skills-eval:
	go test -tags=skills -count=1 -v -run TestSkillEvals ./internal/skills/...

.PHONY: test build recorder skills-eval
