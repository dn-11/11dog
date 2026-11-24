VERSION ?= 1.0.0
LDFLAGS := -s -w -X github.com/11dog/11dog/internal/server.Version=$(VERSION) -X github.com/11dog/11dog/internal/agent.Version=$(VERSION)

.PHONY: proto web build-server build-agent build-agent-linux test helm-lint clean

proto:
	cd proto && buf lint && buf generate

web:
	cd web && npm install && npm run build

build-server: web
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/11dog-server ./cmd/server

build-agent:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/11dog-agent ./cmd/agent

build-agent-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/11dog-agent-linux-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/11dog-agent-linux-arm64 ./cmd/agent

test:
	go test -v ./...

helm-lint:
	helm lint deploy/helm/11dog
	helm template 11dog deploy/helm/11dog >/dev/null

clean:
	rm -rf bin/ web/dist/*
	touch web/dist/.gitkeep
