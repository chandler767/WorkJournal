test:
	go test ./...

# The sherpa-onnx binding links two dylibs and gives the binary an rpath into
# the Go module cache. `make build` copies the dylibs next to bin/journal,
# adds an @executable_path rpath, removes the module-cache rpath and re-signs
# (ad hoc), so bin/journal loads its own copies and survives
# `go clean -modcache`. (Plain `go build` also works, but that binary loads
# the dylibs from the module cache.)
SHERPA_ARCH := $(if $(filter arm64,$(shell uname -m)),aarch64-apple-darwin,x86_64-apple-darwin)
SHERPA_LIB = $(shell go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-macos)/lib/$(SHERPA_ARCH)

build:
	mkdir -p bin
	cp -f "$(SHERPA_LIB)/libsherpa-onnx-c-api.dylib" "$(SHERPA_LIB)/libonnxruntime.dylib" bin/
	go build -ldflags='-extldflags "-Wl,-rpath,@executable_path"' -o bin/journal ./cmd/journal
	install_name_tool -delete_rpath "$(SHERPA_LIB)" bin/journal
	codesign -f -s - bin/journal

recorder:
	./recorder/build.sh

skills-eval:
	go test -tags=skills -count=1 -v -run TestSkillEvals ./internal/skills/...

.PHONY: test build recorder skills-eval
