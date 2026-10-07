test:
	go test ./...

build:
	go build -o bin/journal ./cmd/journal

recorder:
	./recorder/build.sh

skills-eval:
	go test -tags=skills -count=1 -v -run TestSkillEvals ./internal/skills/...

.PHONY: test build recorder skills-eval
