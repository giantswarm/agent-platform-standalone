# Repo-specific targets. The root Makefile includes every Makefile.*.mk, so
# these survive a devctl regeneration of the root file.

CHART_DIR ?= helm/agent-platform-standalone
KUBE_VERSION ?= 1.31.0
RENDER_FILES := $(wildcard examples/*.yaml) $(wildcard $(CHART_DIR)/ci/*.yaml)
TEMPLATE := helm template agent-platform $(CHART_DIR) --kube-version $(KUBE_VERSION)

##@ Curation

.PHONY: curate
curate: ## Regenerate Chart.yaml, values.yaml and Chart.lock from the fleet chart.
	hack/curate.sh

.PHONY: deps
deps: ## Pull the pinned dependencies (helm dependency build, never update).
	helm dependency build $(CHART_DIR)

##@ Verification

.PHONY: verify
verify: test verify-curate verify-render verify-schema ## Everything CI runs.

.PHONY: test
test: ## Unit tests of the generator.
	go test ./...

.PHONY: verify-curate
verify-curate: ## Fail when the generated files or Chart.lock are stale.
	hack/curate.sh --check

.PHONY: verify-render
verify-render: deps ## Render every example and ci values file.
	@for f in $(RENDER_FILES); do \
		echo "--> render $$f"; \
		$(TEMPLATE) -f $$f >/dev/null || exit 1; \
	done
	@echo "--> render with the cloudnative-pg extra dependency on"
	@$(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set components.cloudnative-pg.enabled=true \
		| grep -q '^  name: cnpg-controller-manager-config' || { echo "FAIL: cloudnative-pg did not render"; exit 1; }
	@echo "--> no alias in Chart.yaml"
	@! grep -q '^\s*alias:' $(CHART_DIR)/Chart.yaml || { echo "FAIL: alias found in Chart.yaml"; exit 1; }

.PHONY: verify-schema
verify-schema: deps ## The umbrella schema is strict for its own keys and opaque for component blocks.
	@echo "--> unknown umbrella key must fail"
	@if $(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set notAnUmbrellaKey=1 >/dev/null 2>&1; then \
		echo "FAIL: unknown top-level key accepted"; exit 1; fi
	@echo "--> unknown components entry must fail"
	@if $(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set components.notADependency.enabled=true >/dev/null 2>&1; then \
		echo "FAIL: unknown components entry accepted"; exit 1; fi
	@echo "--> unknown key inside a component block must pass (validated by the component chart)"
	@$(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set kagent.kagent.newUpstreamKey=1 >/dev/null
