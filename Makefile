VERSION ?= dev
DATA ?= ./data
# KUBECONFIG_FILE overrides $KUBECONFIG for make run (the demo sets it).
KUBECONFIG_FILE ?=
DEMO_CONTEXT = kind-opstide-demo
DEMO_KUBECONFIG = ./data-demo/kubeconfig

.PHONY: build web test lint run image perf demo-local demo-up demo demo-install demo-rollout demo-down

# The web app, built into internal/webui/dist and embedded by build.
web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/opstide ./cmd/opstide

test:
	go test -race ./...
	cd web && npm test

# Times the read API on a synthetic big cluster (500 CronJobs, 30 days, about
# 1.1 million log entries; takes a couple of minutes and ~500 MB of disk).
# OPSTIDE_PERF_OPS and OPSTIDE_PERF_DAYS scale it; OPSTIDE_PERF_DIR keeps the
# database between runs; OPSTIDE_PERF_ONLY="within=7d" times one request.
perf:
	OPSTIDE_PERF=1 go test -count=1 -run PerfLargeCluster -v -timeout 40m ./internal/api

lint:
	test -z "$$(gofmt -l .)"
	golangci-lint run
	cd web && npm run lint && npm run typecheck

# Runs against your kubeconfig (all files in KUBECONFIG, merged like
# kubectl) and its current context, or CONTEXT=name. Data goes to $(DATA).
# OpsTide only reads from the cluster. The UI is whatever make web built
# last; for live UI changes run "npm run dev" in web/ alongside.
run:
	go run ./cmd/opstide -kubeconfig "$(or $(KUBECONFIG_FILE),$${KUBECONFIG:-$$HOME/.kube/config})" $(if $(CONTEXT),-context "$(CONTEXT)") -data-dir "$(DATA)" -listen 127.0.0.1:8080

image:
	docker build --build-arg VERSION=$(VERSION) -t opstide:$(VERSION) .

# The quickest look: made-up data, no cluster, Docker or kind. Open http://127.0.0.1:8080.
# Everything is a snapshot taken when it starts; nothing is collected.
demo-local: $(if $(wildcard internal/webui/dist/index.html),,web)
	go run ./cmd/opstide -demo

# A local demo: a kind cluster with CronJobs that run every few minutes,
# some failing, one slow, one suspended, one undescribed. Needs Docker and
# kind. make demo-up once, then make demo and open http://127.0.0.1:8080.
# The demo reaches the cluster through its own kubeconfig, taken from kind,
# so it works whatever KUBECONFIG holds.
demo-up:
	kind get clusters 2>/dev/null | grep -qx opstide-demo || kind create cluster --config demo/kind.yaml
	mkdir -p ./data-demo
	kind get kubeconfig --name opstide-demo > $(DEMO_KUBECONFIG)
	kubectl --kubeconfig $(DEMO_KUBECONFIG) apply -f demo/cronjobs.yaml

demo: $(if $(wildcard internal/webui/dist/index.html),,web)
	@kind get clusters 2>/dev/null | grep -qx opstide-demo || { echo "No opstide-demo cluster yet: run make demo-up first." >&2; exit 1; }
	mkdir -p ./data-demo
	kind get kubeconfig --name opstide-demo > $(DEMO_KUBECONFIG)
	$(MAKE) run KUBECONFIG_FILE=$(DEMO_KUBECONFIG) CONTEXT=$(DEMO_CONTEXT) DATA=./data-demo

# A rollout to see on Today and in the Log.
demo-rollout:
	kubectl --kubeconfig $(DEMO_KUBECONFIG) -n payments rollout restart deployment/payments-api

# OpsTide inside the demo cluster, installed with the chart from an image
# built here (nothing is pulled from a registry). Then:
#   kubectl --kubeconfig ./data-demo/kubeconfig -n opstide port-forward svc/opstide 8080:80
demo-install:
	docker build --build-arg VERSION=demo -t opstide:demo .
	kind load docker-image opstide:demo --name opstide-demo
	helm --kubeconfig $(DEMO_KUBECONFIG) upgrade --install opstide charts/opstide -n opstide --create-namespace \
		--set image.repository=opstide --set image.tag=demo --set image.pullPolicy=Never --set clusterName=opstide-demo
	kubectl --kubeconfig $(DEMO_KUBECONFIG) -n opstide rollout status deployment/opstide --timeout=120s

demo-down:
	kind delete cluster --name opstide-demo
