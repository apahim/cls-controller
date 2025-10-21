# CLS Controller Makefile
# Build, deploy, and manage the generalized CLS controller

# Configuration
PROJECT_ID ?= apahim-dev-1
CLUSTER_NAME ?= rc-1
CLUSTER_ZONE ?= us-central1-a
IMAGE_NAME = cls-controller
IMAGE_TAG ?= $(shell date +%Y%m%d%H%M%S)
FULL_IMAGE_NAME ?= gcr.io/$(PROJECT_ID)/$(IMAGE_NAME):$(IMAGE_TAG)
LATEST_IMAGE_NAME = gcr.io/$(PROJECT_ID)/$(IMAGE_NAME):latest

# Colors for output
BLUE = \033[0;34m
GREEN = \033[0;32m
YELLOW = \033[1;33m
RED = \033[0;31m
NC = \033[0m # No Color

.PHONY: help build push deploy test cleanup all dev-setup config go-build go-test go-lint

# Default target
all: build push deploy

help: ## Show this help message
	@echo "$(BLUE)CLS Controller Build & Deployment$(NC)"
	@echo "=================================="
	@echo ""
	@echo "$(GREEN)Available targets:$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(BLUE)%-15s$(NC) %s\n", $$1, $$2}'
	@echo ""
	@echo "$(GREEN)Configuration:$(NC)"
	@echo "  PROJECT_ID:    $(PROJECT_ID)"
	@echo "  CLUSTER_NAME:  $(CLUSTER_NAME)"
	@echo "  CLUSTER_ZONE:  $(CLUSTER_ZONE)"
	@echo "  IMAGE_NAME:    $(FULL_IMAGE_NAME)"

build: ## Build the container image with podman
	@echo "$(BLUE)📋 Building container image$(NC)"
	@echo "Image: $(FULL_IMAGE_NAME)"
	@echo "$(BLUE)📋 Authenticating with GCR$(NC)"
	@gcloud auth print-access-token | /opt/podman/bin/podman login -u oauth2accesstoken --password-stdin gcr.io
	@echo "$(BLUE)📋 Building with podman$(NC)"
	@cd .. && REGISTRY_AUTH_FILE=/Users/asegundo/.config/containers/auth.json \
		/opt/podman/bin/podman build \
		--platform linux/amd64 \
		-f cls-controller/Dockerfile \
		-t "$(FULL_IMAGE_NAME)" \
		-t "$(LATEST_IMAGE_NAME)" \
		.
	@echo "export CLS_CONTROLLER_IMAGE='$(FULL_IMAGE_NAME)'" > .image-env
	@echo "$(GREEN)✅ Image built: $(FULL_IMAGE_NAME)$(NC)"

push: ## Push the container image to GCR
	@echo "$(BLUE)📋 Pushing image to GCR$(NC)"
	@if [ ! -f ".image-env" ]; then \
		echo "$(RED)❌ No image built. Run 'make build' first$(NC)"; \
		exit 1; \
	fi
	@echo "$(BLUE)📋 Authenticating with GCR$(NC)"
	@gcloud auth print-access-token | /opt/podman/bin/podman login -u oauth2accesstoken --password-stdin gcr.io
	@echo "$(BLUE)📋 Pushing images$(NC)"
	@REGISTRY_AUTH_FILE=/Users/asegundo/.config/containers/auth.json \
		/opt/podman/bin/podman push "$(FULL_IMAGE_NAME)"
	@REGISTRY_AUTH_FILE=/Users/asegundo/.config/containers/auth.json \
		/opt/podman/bin/podman push "$(LATEST_IMAGE_NAME)"
	@echo "$(GREEN)✅ Image pushed: $(FULL_IMAGE_NAME)$(NC)"

configure-kubectl: ## Configure kubectl for the GKE cluster
	@echo "$(BLUE)📋 Configuring kubectl for GKE cluster$(NC)"
	@gcloud container clusters get-credentials "$(CLUSTER_NAME)" --zone="$(CLUSTER_ZONE)" --project="$(PROJECT_ID)"
	@echo "$(GREEN)✅ kubectl configured for cluster $(CLUSTER_NAME)$(NC)"

deploy: configure-kubectl ## Deploy the controller to GKE
	@echo "$(BLUE)📋 Deploying CLS Controller to GKE$(NC)"
	@if [ ! -f ".image-env" ]; then \
		echo "$(YELLOW)⚠️  No .image-env found, using latest tag$(NC)"; \
		export CLS_CONTROLLER_IMAGE="$(LATEST_IMAGE_NAME)"; \
	else \
		source .image-env; \
	fi; \
	echo "Using image: $$CLS_CONTROLLER_IMAGE"; \
	kubectl apply -f config/crds/controllerconfig_crd.yaml && \
	kubectl apply -f deployments/namespace.yaml && \
	kubectl apply -f deployments/rbac.yaml && \
	kubectl apply -f deployments/configmap.yaml && \
	sed "s|gcr.io/apahim-dev-1/cls-controller:latest|$$CLS_CONTROLLER_IMAGE|g" deployments/deployment.yaml | kubectl apply -f - && \
	kubectl apply -f config/examples/echo-job.yaml
	@echo "$(GREEN)✅ Controller deployed$(NC)"

wait-ready: ## Wait for the controller to be ready
	@echo "$(BLUE)📋 Waiting for controller to be ready$(NC)"
	@kubectl wait --for=condition=available deployment/cls-controller -n cls-system --timeout=300s
	@echo "$(GREEN)✅ Controller is ready!$(NC)"

status: configure-kubectl ## Show controller status
	@echo "$(BLUE)📊 Controller Status$(NC)"
	@echo "==================="
	@echo ""
	@echo "$(GREEN)Deployment Status:$(NC)"
	@kubectl get deployment cls-controller -n cls-system -o wide
	@echo ""
	@echo "$(GREEN)Pod Status:$(NC)"
	@kubectl get pods -n cls-system -l app.kubernetes.io/name=cls-controller
	@echo ""
	@echo "$(GREEN)ControllerConfig Status:$(NC)"
	@kubectl get controllerconfig echo-test -n cls-system -o yaml
	@echo ""
	@echo "$(GREEN)Recent Jobs:$(NC)"
	@kubectl get jobs -l test-job=true

logs: configure-kubectl ## Show controller logs
	@echo "$(BLUE)📋 Controller Logs$(NC)"
	@kubectl logs -n cls-system -l app.kubernetes.io/name=cls-controller --tail=50

logs-follow: configure-kubectl ## Follow controller logs
	@echo "$(BLUE)📋 Following Controller Logs (Ctrl+C to exit)$(NC)"
	@kubectl logs -n cls-system -l app.kubernetes.io/name=cls-controller -f

test: configure-kubectl ## Run a full deployment test
	@echo "$(BLUE)🧪 Running deployment test$(NC)"
	@$(MAKE) deploy
	@$(MAKE) wait-ready
	@$(MAKE) status
	@echo ""
	@echo "$(GREEN)🚀 Test completed! Controller is deployed and ready.$(NC)"
	@echo ""
	@echo "$(YELLOW)📝 Next steps:$(NC)"
	@echo "  1. Monitor logs: make logs-follow"
	@echo "  2. Send a cluster event to test the echo job"
	@echo "  3. Check jobs: kubectl get jobs -l test-job=true"

cleanup: configure-kubectl ## Clean up the deployment
	@echo "$(BLUE)🧹 Cleaning up CLS Controller deployment$(NC)"
	@kubectl delete jobs -l test-job=true --ignore-not-found=true
	@kubectl delete -f config/examples/echo-job.yaml --ignore-not-found=true
	@kubectl delete -f deployments/deployment.yaml --ignore-not-found=true
	@kubectl delete -f deployments/configmap.yaml --ignore-not-found=true
	@kubectl delete -f deployments/rbac.yaml --ignore-not-found=true
	@kubectl delete -f config/crds/controllerconfig_crd.yaml --ignore-not-found=true
	@echo "$(YELLOW)⚠️  Keeping namespace cls-system (may contain other resources)$(NC)"
	@echo "$(GREEN)✅ Cleanup complete$(NC)"

clean-local: ## Clean up local build artifacts
	@echo "$(BLUE)🧹 Cleaning local build artifacts$(NC)"
	@rm -f .image-env
	@rm -f controller
	@echo "$(GREEN)✅ Local cleanup complete$(NC)"

# Development targets

go-build: dev-setup ## Build the Go binary locally
	@echo "$(BLUE)📋 Building Go binary$(NC)"
	@go build -o controller cmd/controller/main.go
	@echo "$(GREEN)✅ Binary built: ./controller$(NC)"

go-test: dev-setup ## Run Go tests
	@echo "$(BLUE)🧪 Running Go tests$(NC)"
	@go test ./...
	@echo "$(GREEN)✅ Tests passed$(NC)"

go-lint: dev-setup ## Run Go linter
	@echo "$(BLUE)🔍 Running Go linter$(NC)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "$(YELLOW)⚠️  golangci-lint not found, skipping$(NC)"; \
	fi

# Utility targets

shell: configure-kubectl ## Open a shell in the controller pod
	@echo "$(BLUE)📋 Opening shell in controller pod$(NC)"
	@kubectl exec -it -n cls-system deployment/cls-controller -- /bin/sh

describe-pod: configure-kubectl ## Describe the controller pod
	@kubectl describe pod -n cls-system -l app.kubernetes.io/name=cls-controller

restart: configure-kubectl ## Restart the controller deployment
	@echo "$(BLUE)🔄 Restarting controller$(NC)"
	@kubectl rollout restart deployment/cls-controller -n cls-system
	@kubectl rollout status deployment/cls-controller -n cls-system
	@echo "$(GREEN)✅ Controller restarted$(NC)"

# Quick development cycle
dev: go-build build push deploy status ## Full development cycle: build, push, deploy, status

# Show configuration
config: ## Show current configuration
	@echo "$(BLUE)Configuration$(NC)"
	@echo "============="
	@echo "PROJECT_ID:    $(PROJECT_ID)"
	@echo "CLUSTER_NAME:  $(CLUSTER_NAME)"
	@echo "CLUSTER_ZONE:  $(CLUSTER_ZONE)"
	@echo "IMAGE_NAME:    $(IMAGE_NAME)"
	@echo "IMAGE_TAG:     $(IMAGE_TAG)"
	@echo "FULL_IMAGE:    $(FULL_IMAGE_NAME)"
	@echo "LATEST_IMAGE:  $(LATEST_IMAGE_NAME)"