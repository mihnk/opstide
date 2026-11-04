VERSION ?= dev

.PHONY: build web test lint run image

# The web app, built into internal/webui/dist and embedded by build.
web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/opstide ./cmd/opstide

test:
	go test -race ./...
	cd web && npm test

lint:
	test -z "$$(gofmt -l .)"
	golangci-lint run
	cd web && npm run lint && npm run typecheck

# Runs against your kubeconfig (all files in KUBECONFIG, merged like
# kubectl) and its current context, or CONTEXT=name. Data goes to ./data.
# OpsTide only reads from the cluster. The UI is whatever make web built
# last; for live UI changes run "npm run dev" in web/ alongside.
run:
	go run ./cmd/opstide -kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" $(if $(CONTEXT),-context "$(CONTEXT)") -data-dir ./data -listen 127.0.0.1:8080

image:
	docker build --build-arg VERSION=$(VERSION) -t opstide:$(VERSION) .
