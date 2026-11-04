VERSION ?= dev

.PHONY: build test lint run image

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/opstide ./cmd/opstide

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	golangci-lint run

# Runs against your kubeconfig (all files in KUBECONFIG, merged like
# kubectl) and its current context, or CONTEXT=name. Data goes to ./data.
# OpsTide only reads from the cluster.
run:
	go run ./cmd/opstide -kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" $(if $(CONTEXT),-context "$(CONTEXT)") -data-dir ./data -listen 127.0.0.1:8080

image:
	docker build --build-arg VERSION=$(VERSION) -t opstide:$(VERSION) .
