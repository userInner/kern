.PHONY: build web sdk fmt fmt-check test race fuzz check clean

STATICCHECK_VERSION ?= v0.8.1
GOVULNCHECK_VERSION ?= v1.7.0

build:
	mkdir -p bin
	go build -trimpath -ldflags "-X github.com/userInner/kern/internal/transport/cli.Version=$${VERSION:-dev}" -o bin/kern ./cmd/kern

web:
	cd web && npm ci && npm run build

sdk:
	cd sdk/typescript && npm ci && npm run build

fmt:
	gofmt -w cmd internal sdk/kern sdk/embedded

fmt-check:
	@files="$$(gofmt -l cmd internal sdk/kern sdk/embedded)"; \
	if [ -n "$$files" ]; then \
		echo "The following Go files need gofmt:"; \
		echo "$$files"; \
		exit 1; \
	fi

test:
	go test ./...
	cd sdk/typescript && npm run build && npm test

race:
	go test -race ./...

fuzz:
	go test ./internal/model/compatible -run=^$$ -fuzz=^FuzzRollingSecretRedactor$$ -fuzztime=$${FUZZTIME:-10s}
	go test ./internal/jsonschema -run=^$$ -fuzz=^FuzzCompileAndValidate$$ -fuzztime=$${FUZZTIME:-10s}
	go test ./internal/plugin -run=^$$ -fuzz=^FuzzValidateManifest$$ -fuzztime=$${FUZZTIME:-10s}
	go test ./internal/workspace -run=^$$ -fuzz=^FuzzCleanPathConfinement$$ -fuzztime=$${FUZZTIME:-10s}

check:
	$(MAKE) fmt-check
	go mod verify
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	go test ./...
	go run ./cmd/kern eval validate --output json ./evals/go >/dev/null
	cd web && npm ci && npm audit --audit-level=high && npm run build && npm test
	cd sdk/typescript && npm ci && npm audit --audit-level=high && npm run build && npm test

clean:
	rm -f bin/kern
