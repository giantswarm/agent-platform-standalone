# Repo-specific targets. The root Makefile includes every Makefile.*.mk, so
# these survive a devctl regeneration of the root file.

CHART_DIR ?= helm/agent-platform-standalone
KUBE_VERSION ?= 1.31.0
RENDER_FILES := $(wildcard examples/*.yaml) $(wildcard $(CHART_DIR)/ci/*.yaml)
TEMPLATE := helm template agent-platform $(CHART_DIR) --kube-version $(KUBE_VERSION)
# The vanilla defaults plus the inputs every install needs (a domain, an IdP,
# a public Gateway); everything else stays at its default.
VANILLA := $(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set 'components.klaus-gateway.enabled=false' --set 'components.dicebear.enabled=false' --set 'components.agent-sandbox.enabled=false'
# The modelServing component on, against a cluster that has KServe: helm
# template learns the cluster's APIs only from --api-versions.
KSERVE_API := --api-versions serving.kserve.io/v1alpha1 --api-versions serving.kserve.io/v1beta1
MODEL_SERVING := $(VANILLA) --set components.modelServing.enabled=true $(KSERVE_API)

##@ Curation

.PHONY: curate
curate: ## Regenerate Chart.yaml, values.yaml, Chart.lock, the templates and examples/giantswarm.yaml from the fleet chart.
	hack/curate.sh

.PHONY: deps
deps: ## Pull the pinned dependencies (helm dependency build, never update).
	helm dependency build $(CHART_DIR)

##@ Verification

.PHONY: verify
verify: test verify-curate verify-render verify-schema verify-decisions verify-model-serving ## Everything CI runs.

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

.PHONY: verify-decisions
verify-decisions: deps ## The rendered objects express the vanilla defaults and the opt-ins.
	@echo "--> vanilla default: zero policy/monitoring objects, no OTLP endpoint; hostnames derive from global.domain, routes attach to global.gatewayApi.parentRefs"
	@out=$$($(VANILLA)); \
	for pattern in 'kyverno.io' 'kind: ServiceMonitor' 'kind: PodMonitor' 'kind: CiliumNetworkPolicy' 'kind: NetworkPolicy' 'OTEL_EXPORTER_OTLP_HEADERS'; do \
		if printf '%s' "$$out" | grep -q "$$pattern"; then echo "FAIL: default render contains $$pattern"; exit 1; fi; \
	done; \
	if printf '%s' "$$out" | grep -A1 'OTEL_EXPORTER_OTLP_ENDPOINT' | grep -Eq '(OTEL_EXPORTER_OTLP_ENDPOINT|value): *"?[^" ]'; then \
		echo "FAIL: default render carries an OTLP endpoint"; exit 1; fi; \
	printf '%s' "$$out" | grep -q 'muster.ci.example.com' || { echo "FAIL: muster hostname not derived"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'agentgateway.ci.example.com' || { echo "FAIL: kagent controller hostname not derived"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'backstage.ci.example.com' || { echo "FAIL: backstage hostname not derived"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'name: giantswarm-default' || { echo "FAIL: routes do not attach to global.gatewayApi.parentRefs"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'request: 0s' || { echo "FAIL: HTTPRoute timeouts missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'kind: BackendTrafficPolicy' && { echo "FAIL: Envoy BackendTrafficPolicy rendered by default"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'name: agent-platform-backstage-app-config' || { echo "FAIL: backstage app-config not rendered"; exit 1; }
	@echo "--> examples/giantswarm.yaml turns Kyverno, Cilium, ServiceMonitors, the valkey PodMonitor and OTLP back on"
	@out=$$($(TEMPLATE) -f examples/giantswarm.yaml); \
	for pattern in 'kyverno.io' 'kind: ServiceMonitor' 'kind: PodMonitor' 'OTEL_EXPORTER_OTLP_ENDPOINT' 'kind: CiliumNetworkPolicy'; do \
		printf '%s' "$$out" | grep -q "$$pattern" || { echo "FAIL: giantswarm render lacks $$pattern"; exit 1; }; \
	done
	@echo "--> kubernetes network-policy flavor renders NetworkPolicy, no CiliumNetworkPolicy"
	@out=$$($(VANILLA) --set global.networkPolicy.enabled=true); \
	printf '%s' "$$out" | grep -q 'kind: NetworkPolicy' || { echo "FAIL: no NetworkPolicy"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'kind: CiliumNetworkPolicy' && { echo "FAIL: CiliumNetworkPolicy under kubernetes flavor"; exit 1; }; true
	@echo "--> gatewayApi.gateway.create renders the edge listener and no layer-1 route"
	@out=$$($(TEMPLATE) -f examples/kind-lab-dex.yaml); \
	printf '%s' "$$out" | grep -q 'hostname: "\*.127.0.0.1.nip.io"' || { echo "FAIL: edge listener missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'sectionName: https' || { echo "FAIL: public routes not pinned to the HTTPS listener"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'name: kagent-controller-public' && { echo "FAIL: layer-1 kagent route rendered with the edge as data plane"; exit 1; }; true
	@echo "--> guards: gateway.create needs the certificate; a diverging muster issuer or kagent UI hostname fails"
	@if $(TEMPLATE) -f examples/kind-lab-dex.yaml --set gatewayApi.gateway.tls.secretName= >/dev/null 2>&1; then \
		echo "FAIL: gateway.create without tls.secretName accepted"; exit 1; fi
	@if $(VANILLA) --set muster.muster.oauth.server.dex.issuerUrl=https://other.example.com >/dev/null 2>&1; then \
		echo "FAIL: muster issuer differing from global.identity accepted"; exit 1; fi
	@if $(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set global.domain= --set 'global.gatewayApi.parentRefs=null' >/dev/null 2>&1; then \
		echo "FAIL: no domain and no Gateway accepted"; exit 1; fi
	@out=$$($(VANILLA) --set components.kagent.uiRoute.hostname=ui.example.com --set kagent.kagent.oauth2-proxy.enabled=true 2>&1); \
	printf '%s' "$$out" | grep -q 'redirect-url still derives from global.domain' || { \
		echo "FAIL: overridden kagent UI hostname with the derived redirect-url accepted"; exit 1; }
	@echo "--> guards: an explicit legacy klaus-gateway.enabled=false fails; a true is indistinguishable from the coalesced default and passes"
	@out=$$($(VANILLA) --set components.klaus-gateway.enabled=true --set klaus-gateway.enabled=false 2>&1) && { \
		echo "FAIL: klaus-gateway.enabled=false accepted while components.klaus-gateway.enabled=true"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'move klaus-gateway.enabled -> components.klaus-gateway.enabled' || { \
		echo "FAIL: render failed for another reason than the legacy-toggle guard"; exit 1; }
	@out=$$($(VANILLA) --set klaus-gateway.enabled=false 2>&1) && { \
		echo "FAIL: klaus-gateway.enabled=false accepted while the component is off"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'move klaus-gateway.enabled -> components.klaus-gateway.enabled' || { \
		echo "FAIL: render failed for another reason than the legacy-toggle guard"; exit 1; }
	@# klaus-gateway.enabled=true must NOT fail: the klaus-gateway chart's own
	@# `enabled: true` default reaches .Values through Helm's coalescing while
	@# the dependency is on, and through chart-operator's coalesced override
	@# values even while it is off (the chart installed as a Giant Swarm App —
	@# the ATS kind smoke), so the guard cannot tell it from an operator's.
	@$(VANILLA) --set klaus-gateway.enabled=true >/dev/null || { \
		echo "FAIL: klaus-gateway.enabled=true rejected; chart-operator installs carry that key for the disabled component"; exit 1; }

.PHONY: verify-model-serving
verify-model-serving: deps ## The modelServing component renders nothing while off, the runtime/presets/cache while on, and every published preset validates against the schema.
	@echo "--> modelServing off (the default): the render carries no model-serving object"
	@out=$$($(VANILLA)); \
	for pattern in 'serving.kserve.io' 'agent-platform.giantswarm.io/serving-preset' 'agent-platform-model-serving' 'name: hf-cache' 'model-serving'; do \
		if printf '%s' "$$out" | grep -q "$$pattern"; then echo "FAIL: default render contains $$pattern"; exit 1; fi; \
	done
	@echo "--> modelServing on without the KServe API fails with the prerequisite message; requireApi=false skips the check"
	@out=$$($(VANILLA) --set components.modelServing.enabled=true 2>&1) && { \
		echo "FAIL: modelServing rendered without the serving.kserve.io API"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'KServe CRDs' || { echo "FAIL: render failed for another reason than the KServe guard"; exit 1; }
	@$(VANILLA) --set components.modelServing.enabled=true --set components.modelServing.kserve.requireApi=false >/dev/null
	@echo "--> modelServing on: runtime, namespace, discovery ConfigMap, presets, cache PVC; no Kyverno object without policies.enabled"
	@out=$$($(MODEL_SERVING)); \
	for pattern in 'kind: ClusterServingRuntime' 'name: kserve-vllm' 'name: agent-platform-model-serving' 'kind: PersistentVolumeClaim' 'name: hf-cache' 'agent-platform-serving-preset-qwen3-8-27b' 'name: agent-platform-chat-template-qwen3-8-27b' 'name: model-serving'; do \
		printf '%s' "$$out" | grep -q "$$pattern" || { echo "FAIL: modelServing render lacks $$pattern"; exit 1; }; \
	done; \
	printf '%s' "$$out" | grep -q 'kyverno.io' && { echo "FAIL: Kyverno object rendered without policies.enabled"; exit 1; }; \
	printf '%s' "$$out" | go run ./hack/presets -render -
	@echo "--> policies.enabled renders the cache and deployment ClusterPolicies; cache.pvc.existingClaim drops the PVC"
	@out=$$($(MODEL_SERVING) --set components.modelServing.policies.enabled=true --set components.modelServing.cache.pvc.existingClaim=models); \
	printf '%s' "$$out" | grep -q 'name: agent-platform-standalone-model-serving-pods' || { echo "FAIL: pod policy missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'name: agent-platform-standalone-model-serving-deployments' || { echo "FAIL: deployment policy missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'claimName: models' || { echo "FAIL: existing claim not used"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'templates/model-serving/cache-pvc.yaml' && { echo "FAIL: PVC rendered next to an existing claim"; exit 1; }; true
	@echo "--> a values preset replaces the shipped one of the same name; shippedPresets.enabled=false drops the shipped set; a bad preset fails"
	@out=$$($(MODEL_SERVING) -f hack/testdata/model-serving-values.yaml); \
	printf '%s' "$$out" | grep -q 'displayName: Overridden' || { echo "FAIL: values preset did not replace the shipped one"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'agent-platform.giantswarm.io/preset-source: "values"' || { echo "FAIL: values preset not labelled as such"; exit 1; }; \
	printf '%s' "$$out" | go run ./hack/presets -render - >/dev/null
	@out=$$($(MODEL_SERVING) --set components.modelServing.shippedPresets.enabled=false); \
	printf '%s' "$$out" | grep -q 'agent-platform.giantswarm.io/serving-preset: "true"' && { echo "FAIL: shipped presets rendered while disabled"; exit 1; }; true
	@out=$$($(MODEL_SERVING) --set 'components.modelServing.shippedPresets.exclude={qwen3-14b}'); \
	printf '%s' "$$out" | grep -q 'agent-platform-serving-preset-qwen3-14b' && { echo "FAIL: excluded preset rendered"; exit 1; }; true
	@if $(MODEL_SERVING) --set 'components.modelServing.presets[0].metadata.name=bad' >/dev/null 2>&1; then \
		echo "FAIL: a preset without spec accepted"; exit 1; fi
	@echo "--> the shipped preset files validate against the schema (also run by go test)"
	@go run ./hack/presets >/dev/null
