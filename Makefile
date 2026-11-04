VERSION ?= dev
DATA ?= ./data
DEMO_CONTEXT = kind-opstide-demo

.PHONY: build web test lint run image demo-up demo demo-rollout demo-down

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
# kubectl) and its current context, or CONTEXT=name. Data goes to $(DATA).
# OpsTide only reads from the cluster. The UI is whatever make web built
# last; for live UI changes run "npm run dev" in web/ alongside.
run:
	go run ./cmd/opstide -kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" $(if $(CONTEXT),-context "$(CONTEXT)") -data-dir "$(DATA)" -listen 127.0.0.1:8080

image:
	docker build --build-arg VERSION=$(VERSION) -t opstide:$(VERSION) .

# A local demo: a kind cluster with CronJobs that run every few minutes,
# some failing, one slow, one suspended, one undescribed. Needs Docker and
# kind. make demo-up once, then make demo and open http://127.0.0.1:8080.
demo-up:
	kind get clusters 2>/dev/null | grep -qx opstide-demo || kind create cluster --config demo/kind.yaml
	kubectl --context $(DEMO_CONTEXT) apply -f demo/cronjobs.yaml

demo: $(if $(wildcard internal/webui/dist/index.html),,web)
	$(MAKE) run CONTEXT=$(DEMO_CONTEXT) DATA=./data-demo

# A rollout to see on Today and in the Log.
demo-rollout:
	kubectl --context $(DEMO_CONTEXT) -n payments rollout restart deployment/payments-api

demo-down:
	kind delete cluster --name opstide-demo
