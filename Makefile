VERSION ?= dev

.PHONY: build test lint run image

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/opstide ./cmd/opstide

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	golangci-lint run

# Runs against the current kubeconfig context, storing data in ./data.
run:
	go run ./cmd/opstide -kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" -data-dir ./data -listen 127.0.0.1:8080

image:
	docker build --build-arg VERSION=$(VERSION) -t opstide:$(VERSION) .
