REGISTRY           ?= quay.io/javierpena
CONTROLLER_IMAGE   ?= $(REGISTRY)/telco-anomaly-controller:latest
ALERTRECEIVER_IMAGE ?= $(REGISTRY)/telco-anomaly-alert-receiver:latest
SKILLS_IMAGE       ?= $(REGISTRY)/telco-anomaly-skills:latest

CONTAINER_ENGINE   ?= docker
CONTROLLER_GEN     ?= controller-gen
GOLANGCI_LINT      ?= golangci-lint

# Build binaries
.PHONY: build
build: build-controller build-alertreceiver

.PHONY: build-controller
build-controller:
	go build -o bin/controller ./cmd/controller/...

.PHONY: build-alertreceiver
build-alertreceiver:
	go build -o bin/alertreceiver ./cmd/alertreceiver/...

# Container images
.PHONY: container-build
container-build: container-build-controller container-build-alertreceiver container-build-skills

.PHONY: container-build-controller
container-build-controller:
	$(CONTAINER_ENGINE) build -t $(CONTROLLER_IMAGE) -f Dockerfile.controller .

.PHONY: container-build-alertreceiver
container-build-alertreceiver:
	$(CONTAINER_ENGINE) build -t $(ALERTRECEIVER_IMAGE) -f Dockerfile.alertreceiver .

.PHONY: container-build-skills
container-build-skills:
	$(CONTAINER_ENGINE) build -t $(SKILLS_IMAGE) -f Dockerfile.skills .

.PHONY: container-push
container-push: container-push-controller container-push-alertreceiver container-push-skills

.PHONY: container-push-controller
container-push-controller:
	$(CONTAINER_ENGINE) push $(CONTROLLER_IMAGE)

.PHONY: container-push-alertreceiver
container-push-alertreceiver:
	$(CONTAINER_ENGINE) push $(ALERTRECEIVER_IMAGE)

.PHONY: container-push-skills
container-push-skills:
	$(CONTAINER_ENGINE) push $(SKILLS_IMAGE)

# Code generation
.PHONY: generate
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests:
	$(CONTROLLER_GEN) crd paths="./..." output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=telco-anomaly-operator paths="./..." output:rbac:artifacts:config=config/rbac

# Quality
.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./...

.PHONY: test
test:
	go test ./... -v -count=1

.PHONY: vet
vet:
	go vet ./...

# Dependencies
.PHONY: tidy
tidy:
	go mod tidy

# Install tools
.PHONY: install-tools
install-tools:
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Deploy
.PHONY: install-namespace
install-namespace:
	kubectl apply -f config/manager/namespace.yaml

.PHONY: install-crd
install-crd:
	kubectl apply -f config/crd/bases/

.PHONY: install-rbac
install-rbac: install-namespace
	kubectl apply -f config/rbac/

.PHONY: deploy
deploy: install-namespace install-crd install-rbac
	kubectl apply -f config/manager/

.PHONY: undeploy
undeploy:
	kubectl delete -f config/manager/ --ignore-not-found
	kubectl delete -f config/rbac/ --ignore-not-found
	kubectl delete -f config/crd/bases/ --ignore-not-found
	kubectl delete -f config/manager/namespace.yaml --ignore-not-found

.PHONY: clean
clean:
	rm -rf bin/
