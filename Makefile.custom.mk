# Repo-specific targets. The root Makefile includes every Makefile.*.mk, so
# these survive a devctl regeneration of the root file.

CHART_DIR ?= helm/agent-platform-standalone
KUBE_VERSION ?= 1.31.0
RENDER_FILES := $(wildcard examples/*.yaml) $(wildcard $(CHART_DIR)/ci/*.yaml)
TEMPLATE := helm template agent-platform $(CHART_DIR) --kube-version $(KUBE_VERSION)
# The vanilla defaults plus the inputs every install needs (a domain, an IdP,
# a public Gateway); everything else stays at its default.
VANILLA := $(TEMPLATE) -f $(CHART_DIR)/ci/ci-values.yaml --set 'components.klaus-gateway.enabled=false' --set 'components.dicebear.enabled=false' --set 'components.agent-sandbox.enabled=false' --set 'components.model-manager.enabled=false'
# The modelServing component on, against a cluster that has KServe: helm
# template learns the cluster's APIs only from --api-versions.
KSERVE_API := --api-versions serving.kserve.io/v1alpha1 --api-versions serving.kserve.io/v1beta1
MODEL_SERVING := $(VANILLA) --set components.modelServing.enabled=true $(KSERVE_API)
# The kserve component on, against a cluster that has cert-manager (its one
# prerequisite): the first-install shape, no serving API yet.
CERT_MANAGER_API := --api-versions cert-manager.io/v1
KSERVE := $(VANILLA) --set components.kserve.enabled=true $(CERT_MANAGER_API)
# The llm-d control plane on top (its CRDs and controller come with the
# release), against a cluster with no inference gateway (the controller ships
# the GIE CRDs).
LLMISVC := $(KSERVE) $(KSERVE_API) --set components.kserve.llmisvc.enabled=true
# The model-manager component on with the ollama backend, its route and JWT
# policy (the shape the lab and the portal run).
MODEL_MANAGER := $(VANILLA) --set 'components.model-manager.enabled=true' --set 'components.model-manager.route.enabled=true' \
	--set 'components.model-manager.route.jwtAuthentication.enabled=true' --set gateway.jwksEgress.enabled=true \
	--set 'model-manager.ollama.endpoint=http://192.0.2.10:11434'

##@ Curation

.PHONY: curate
curate: ## Regenerate Chart.yaml, values.yaml, Chart.lock, the templates and examples/giantswarm.yaml from the fleet chart.
	hack/curate.sh

.PHONY: deps
deps: ## Pull the pinned dependencies (helm dependency build, never update).
	helm dependency build $(CHART_DIR)

##@ Verification

.PHONY: verify
verify: test verify-curate verify-render verify-schema verify-decisions verify-model-serving verify-model-manager verify-kserve ## Everything CI runs.

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

	@echo "--> network policies: none without global.networkPolicy; the kubernetes flavor renders the predictor ingress/egress and the download-Job egress; cilium renders toFQDNs behind the DNS proxy rule, the probe allowance and the agent egress"
	@out=$$($(MODEL_SERVING)); \
	printf '%s' "$$out" | grep -q 'model-serving-predictor' && { echo "FAIL: serving network policy rendered without global.networkPolicy.enabled"; exit 1; }; true
	@out=$$($(MODEL_SERVING) --set global.networkPolicy.enabled=true); \
	for pattern in 'name: agent-platform-standalone-model-serving-predictor-ingress' 'name: agent-platform-standalone-model-serving-predictor-egress' 'name: agent-platform-standalone-model-serving-download-egress' 'key: serving.kserve.io/inferenceservice' 'kubernetes.io/metadata.name: kagent' 'model-manager.giantswarm.io/component: download' 'Hugging Face: vanilla NetworkPolicy has no FQDN selector' 'networkPolicy:' 'flavor: kubernetes'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: kubernetes-flavor serving render lacks $$pattern"; exit 1; }; \
	done; \
	printf '%s' "$$out" | grep -q 'kind: CiliumNetworkPolicy' && { echo "FAIL: CiliumNetworkPolicy under kubernetes flavor"; exit 1; }; true
	@out=$$($(MODEL_SERVING) --set global.networkPolicy.enabled=true --set global.networkPolicy.flavor=cilium); \
	for pattern in 'name: agent-platform-standalone-model-serving-predictor$$' 'name: agent-platform-standalone-model-serving-download$$' 'name: agent-platform-standalone-kagent-agents-to-model-serving' 'toFQDNs:' 'matchName: huggingface.co' 'matchPattern: .\*.hf.co' 'matchPattern: "\*"' '- remote-node'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: cilium-flavor serving render lacks $$pattern"; exit 1; }; \
	done; \
	printf '%s' "$$out" | grep -q 'kind: NetworkPolicy' && { echo "FAIL: NetworkPolicy under cilium flavor"; exit 1; }; true
	@echo "--> huggingFace.cidrs replaces the public-destination rule; additionalIngressNamespaces and predictor.port reach the policies; the guards reject a bad FQDN entry, CIDR, namespace and port"
	@out=$$($(MODEL_SERVING) --set global.networkPolicy.enabled=true --set 'components.modelServing.networkPolicy.huggingFace.cidrs={203.0.113.0/24}' --set 'components.modelServing.networkPolicy.predictor.additionalIngressNamespaces={envoy-gateway-system}' --set components.modelServing.networkPolicy.predictor.port=9000); \
	for pattern in 'cidr: "203.0.113.0/24"' 'kubernetes.io/metadata.name: envoy-gateway-system' 'port: 9000'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: serving render lacks $$pattern"; exit 1; }; \
	done; \
	printf '%s' "$$out" | grep -q 'Hugging Face: vanilla NetworkPolicy has no FQDN selector' && { echo "FAIL: public-destination rule rendered next to huggingFace.cidrs"; exit 1; }; true
	@if $(MODEL_SERVING) --set global.networkPolicy.enabled=true --set 'components.modelServing.networkPolicy.huggingFace.fqdns[0].matchName=a.example' --set 'components.modelServing.networkPolicy.huggingFace.fqdns[0].matchPattern=*.example' >/dev/null 2>&1; then \
		echo "FAIL: FQDN entry with both matchName and matchPattern accepted"; exit 1; fi
	@if $(MODEL_SERVING) --set global.networkPolicy.enabled=true --set 'components.modelServing.networkPolicy.huggingFace.cidrs={notacidr}' >/dev/null 2>&1; then \
		echo "FAIL: bad CIDR accepted"; exit 1; fi
	@if $(MODEL_SERVING) --set global.networkPolicy.enabled=true --set 'components.modelServing.networkPolicy.predictor.additionalIngressNamespaces={Bad_NS}' >/dev/null 2>&1; then \
		echo "FAIL: bad namespace name accepted"; exit 1; fi
	@if $(MODEL_SERVING) --set global.networkPolicy.enabled=true --set components.modelServing.networkPolicy.predictor.port=0 >/dev/null 2>&1; then \
		echo "FAIL: port 0 accepted"; exit 1; fi

.PHONY: verify-model-manager
verify-model-manager: deps ## The model-manager component renders nothing while off, the service + route + JWT policy + app-config entry while on, and its guards fail inconsistent configs.
	@echo "--> model-manager off (the default): the render carries no model-manager object"
	@out=$$($(VANILLA)); \
	for pattern in 'model-manager' 'modelManager'; do \
		if printf '%s' "$$out" | grep -q -e "$$pattern"; then echo "FAIL: default render contains $$pattern"; exit 1; fi; \
	done
	@echo "--> model-manager on (ollama backend, route, JWT): Deployment, Service, MCPServer, backends, routes, policy, app-config entry"
	@out=$$($(MODEL_MANAGER)); \
	for pattern in 'kind: Deployment' 'name: model-manager$$' '--backend=ollama' '--ollama-endpoint=http://192.0.2.10:11434' 'kind: MCPServer' 'url: http://model-manager\..*\.svc\.cluster\.local:8080/mcp' \
		'host: model-manager\..*\.svc\.cluster\.local' 'name: model-manager-public' 'name: model-manager-jwks' 'name: model-manager-jwt' 'replacePrefixMatch: /' \
		'apiBaseUrl: https://agentgateway.ci.example.com/model-manager$$' 'name: model-manager-kagent$$'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: model-manager render lacks $$pattern"; exit 1; }; \
	done; \
	printf '%s' "$$out" | grep -q 'kind: NetworkPolicy' && { echo "FAIL: NetworkPolicy rendered without global.networkPolicy.enabled"; exit 1; }; true
	@echo "--> the edge as data plane drops the public route; the JWKS TLS option renders the CA reference"
	@out=$$($(TEMPLATE) -f examples/kind-lab-dex.yaml --set 'components.model-manager.enabled=true' --set 'components.model-manager.route.enabled=true' \
		--set 'components.model-manager.route.jwtAuthentication.enabled=true' --set 'components.model-manager.route.jwtAuthentication.jwks.tls.enabled=true' \
		--set gateway.jwksEgress.enabled=true --set 'model-manager.ollama.endpoint=http://172.21.0.1:11434'); \
	printf '%s' "$$out" | grep -q 'name: model-manager-public' && { echo "FAIL: layer-1 model-manager route rendered with the edge as data plane"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'apiBaseUrl: https://agentgateway.127.0.0.1.nip.io/model-manager$$' || { echo "FAIL: lab apiBaseUrl not rendered"; exit 1; }; \
	printf '%s' "$$out" | grep -A3 'caCertificateRefs:' | grep -q 'name: agent-platform-idp-ca' || { echo "FAIL: JWKS TLS CA reference not rendered from global.identity.ca"; exit 1; }
	@echo "--> network policies: both flavors render the ingress/egress pair and the Ollama /32; a hostname endpoint opens the port"
	@out=$$($(MODEL_MANAGER) --set global.networkPolicy.enabled=true); \
	for pattern in 'name: agent-platform-standalone-model-manager-ingress' 'name: agent-platform-standalone-model-manager-egress' 'name: agent-platform-standalone-dataplane-to-model-manager' 'name: agent-platform-standalone-muster-to-model-manager' 'cidr: 192.0.2.10/32'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: kubernetes-flavor render lacks $$pattern"; exit 1; }; \
	done
	@out=$$($(MODEL_MANAGER) --set global.networkPolicy.enabled=true --set global.networkPolicy.flavor=cilium); \
	printf '%s' "$$out" | grep -q 'kind: CiliumNetworkPolicy' || { echo "FAIL: no CiliumNetworkPolicy"; exit 1; }; \
	printf '%s' "$$out" | grep -q -- '- 192.0.2.10/32' || { echo "FAIL: cilium toCIDR missing"; exit 1; }
	@out=$$($(MODEL_MANAGER) --set global.networkPolicy.enabled=true --set 'model-manager.ollama.endpoint=http://ollama.example.internal:11434'); \
	printf '%s' "$$out" | grep -q 'cidr: 0.0.0.0/0' || { echo "FAIL: hostname endpoint did not open the port"; exit 1; }
	@echo "--> guards: ollama without endpoint, an unknown backend, kserve without KServe, the route in muster-direct mode, JWT without jwksEgress, wiring without kagent, MCPServer without muster"
	@if $(VANILLA) --set 'components.model-manager.enabled=true' --set 'model-manager.ollama.endpoint=' >/dev/null 2>&1; then \
		echo "FAIL: ollama backend without an endpoint accepted"; exit 1; fi
	@if $(VANILLA) --set 'components.model-manager.enabled=true' --set 'model-manager.ollama.endpoint=ollama:11434' >/dev/null 2>&1; then \
		echo "FAIL: ollama endpoint without a scheme accepted"; exit 1; fi
	@if $(MODEL_MANAGER) --set 'model-manager.backend=vllm' >/dev/null 2>&1; then \
		echo "FAIL: unknown backend accepted"; exit 1; fi
	@out=$$($(VANILLA) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' 2>&1) && { \
		echo "FAIL: kserve backend without KServe accepted"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'serving.kserve.io/v1beta1' || { echo "FAIL: render failed for another reason than the KServe guard"; exit 1; }
	@$(VANILLA) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --api-versions serving.kserve.io/v1beta1 >/dev/null
	@$(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' >/dev/null
	@if $(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --set 'model-manager.kserve.namespace=other' >/dev/null 2>&1; then \
		echo "FAIL: kserve namespace differing from modelServing accepted"; exit 1; fi
	@if $(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --set 'model-manager.kserve.discovery.configMap=other' >/dev/null 2>&1; then \
		echo "FAIL: kserve discovery ConfigMap differing from modelServing accepted"; exit 1; fi
	@if $(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --set 'model-manager.kserve.runtime=other' >/dev/null 2>&1; then \
		echo "FAIL: kserve runtime differing from modelServing accepted"; exit 1; fi
	@$(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --set 'model-manager.kserve.runtime=kserve-vllm' --set 'model-manager.kserve.cache.claimName=hf-cache' >/dev/null
	@echo "--> kserve backend: the chart renders the serving-namespace Role and the nodes ClusterRole"
	@out=$$($(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve'); \
	printf '%s' "$$out" | grep -q 'name: model-manager-kserve$$' || { echo "FAIL: kserve Role missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'name: model-manager-nodes$$' || { echo "FAIL: nodes ClusterRole missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q -e '--backend=kserve' || { echo "FAIL: kserve backend arg missing"; exit 1; }
	@if $(MODEL_MANAGER) --set ingress.mode=muster-direct --set components.agentgateway.enabled=false --set 'agent-platform-mcps.agentgateway.viaMuster=false' >/dev/null 2>&1; then \
		echo "FAIL: model-manager route in muster-direct mode accepted"; exit 1; fi
	@if $(MODEL_MANAGER) --set gateway.jwksEgress.enabled=false >/dev/null 2>&1; then \
		echo "FAIL: JWT policy without jwksEgress accepted"; exit 1; fi
	@if $(MODEL_MANAGER) --set components.kagent.enabled=false >/dev/null 2>&1; then \
		echo "FAIL: ModelConfig wiring without the kagent component accepted"; exit 1; fi
	@$(MODEL_MANAGER) --set components.kagent.enabled=false --set 'model-manager.kagent.disableWiring=true' >/dev/null
	@if $(MODEL_MANAGER) --set components.muster.enabled=false --set components.valkey.enabled=false >/dev/null 2>&1; then \
		echo "FAIL: MCPServer CR without the muster component accepted"; exit 1; fi
	@echo "--> kserve backend under network policies: model-manager gets the Hugging Face egress in both flavors"
	@out=$$($(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --set global.networkPolicy.enabled=true); \
	printf '%s' "$$out" | awk '/name: agent-platform-standalone-model-manager-egress/,/^---/' | grep -q 'Hugging Face' || { echo "FAIL: kubernetes-flavor model-manager egress lacks the Hugging Face rule"; exit 1; }
	@out=$$($(MODEL_SERVING) --set 'components.model-manager.enabled=true' --set 'model-manager.backend=kserve' --set global.networkPolicy.enabled=true --set global.networkPolicy.flavor=cilium); \
	printf '%s' "$$out" | awk '/name: agent-platform-standalone-model-manager-egress/,/^---/' | grep -q 'toFQDNs:' || { echo "FAIL: cilium-flavor model-manager egress lacks the Hugging Face FQDN rule"; exit 1; }
	@out=$$($(MODEL_MANAGER) --set global.networkPolicy.enabled=true); \
	printf '%s' "$$out" | awk '/name: agent-platform-standalone-model-manager-egress/,/^---/' | grep -q 'Hugging Face' && { echo "FAIL: ollama backend got the Hugging Face egress"; exit 1; }; true

.PHONY: verify-kserve
verify-kserve: deps ## The kserve component renders nothing while off, the KServe CRDs + controller while on (llmisvc on top), and its guards fail the missing prerequisites and the one-pass first install.
	@echo "--> kserve off (the default): the render carries no KServe control-plane object"
	@out=$$($(VANILLA)); \
	for pattern in 'kind: CustomResourceDefinition' 'kserve-controller-manager' 'llmisvc-controller-manager' 'inferenceservice-config'; do \
		if printf '%s' "$$out" | grep -q -e "$$pattern"; then echo "FAIL: default render contains $$pattern"; exit 1; fi; \
	done
	@echo "--> kserve on: the six KServe CRDs, the controller in Standard mode with no per-model ingress, the webhooks, the cert-manager Issuer; no llmisvc, no network policy without global.networkPolicy"
	@out=$$($(KSERVE)); \
	for pattern in 'name: inferenceservices.serving.kserve.io' 'name: clusterservingruntimes.serving.kserve.io' 'name: servingruntimes.serving.kserve.io' 'name: trainedmodels.serving.kserve.io' 'name: inferencegraphs.serving.kserve.io' 'name: clusterstoragecontainers.serving.kserve.io' 'name: kserve-controller-manager' '"defaultDeploymentMode": "Standard"' '"disableIngressCreation": true' 'kind: MutatingWebhookConfiguration' 'kind: Issuer' 'name: inferenceservice-config'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: kserve render lacks $$pattern"; exit 1; }; \
	done; \
	for pattern in 'llmisvc-controller-manager' 'kind: CiliumNetworkPolicy' 'kind: NetworkPolicy' 'kserve-huggingfaceserver'; do \
		if printf '%s' "$$out" | grep -q -e "$$pattern"; then echo "FAIL: kserve render contains $$pattern"; exit 1; fi; \
	done
	@echo "--> guards: cert-manager missing fails (requireApi=false passes); Knative mode fails; modelServing or a kserve-backend model-manager on the first install fails, and passes once the serving API exists"
	@out=$$($(VANILLA) --set components.kserve.enabled=true 2>&1) && { echo "FAIL: kserve rendered without the cert-manager API"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'cert-manager.io/v1 API is not on the cluster' || { echo "FAIL: render failed for another reason than the cert-manager guard"; exit 1; }
	@$(VANILLA) --set components.kserve.enabled=true --set components.kserve.certManager.requireApi=false >/dev/null
	@out=$$($(KSERVE) --set kserve-resources.kserve.controller.deploymentMode=Knative 2>&1) && { echo "FAIL: Knative mode accepted"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'must be Standard' || { echo "FAIL: render failed for another reason than the deployment-mode guard"; exit 1; }
	@out=$$($(KSERVE) --set components.modelServing.enabled=true 2>&1) && { echo "FAIL: modelServing accepted on the first install of the kserve component"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'first install of the KServe control plane' || { echo "FAIL: render failed for another reason than the two-phase guard"; exit 1; }
	@out=$$($(KSERVE) --set 'components.model-manager.enabled=true' --set model-manager.backend=kserve --set model-manager.kserve.namespace=model-serving 2>&1) && { echo "FAIL: kserve-backend model-manager accepted on the first install of the kserve component"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'first install of the KServe control plane' || { echo "FAIL: render failed for another reason than the two-phase guard"; exit 1; }
	@out=$$($(KSERVE) $(KSERVE_API) --set components.modelServing.enabled=true); \
	printf '%s' "$$out" | grep -q 'name: kserve-vllm' || { echo "FAIL: modelServing did not render next to the kserve component with the serving API present"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'name: kserve-controller-manager' || { echo "FAIL: kserve controller missing in the second-phase render"; exit 1; }
	@echo "--> llmisvc: needs the kserve component; renders the LLMInferenceService CRDs (conversion webhook in the release namespace) and controller with the GIE CRDs and without the shared objects; createGIECRDs=false needs the GIE API; createSharedResources=true fails"
	@out=$$($(VANILLA) $(CERT_MANAGER_API) --set components.kserve.llmisvc.enabled=true 2>&1) && { echo "FAIL: llmisvc accepted without the kserve component"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'components.kserve.enabled is false' || { echo "FAIL: render failed for another reason than the llmisvc-needs-kserve guard"; exit 1; }
	@out=$$($(LLMISVC)); \
	for pattern in 'name: llmisvc-controller-manager' 'name: llminferenceservices.serving.kserve.io' 'name: llminferenceserviceconfigs.serving.kserve.io' 'name: inferencepools.inference.networking.k8s.io' 'name: kserve-controller-manager'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: llmisvc render lacks $$pattern"; exit 1; }; \
	done; \
	[ "$$(printf '%s' "$$out" | grep -c 'name: inferenceservice-config')" = "1" ] || { echo "FAIL: the shared inferenceservice-config must render exactly once"; exit 1; }; \
	[ "$$(printf '%s' "$$out" | grep -c '^kind: Issuer')" = "1" ] || { echo "FAIL: the shared Issuer must render exactly once"; exit 1; }
	@out=$$($(LLMISVC) --set kserve-llmisvc-resources.kserve.llmisvc.createGIECRDs=false 2>&1) && { echo "FAIL: createGIECRDs=false accepted without the GIE API"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'createGIECRDs is false but' || { echo "FAIL: render failed for another reason than the GIE guard"; exit 1; }
	@out=$$($(LLMISVC) --set kserve-llmisvc-resources.kserve.llmisvc.createGIECRDs=false --api-versions inference.networking.k8s.io/v1); \
	printf '%s' "$$out" | grep -q 'name: inferencepools.inference.networking.k8s.io' && { echo "FAIL: GIE CRDs rendered with createGIECRDs=false"; exit 1; }; true
	@out=$$($(LLMISVC) --set kserve-llmisvc-resources.kserve.createSharedResources=true 2>&1) && { echo "FAIL: createSharedResources=true accepted"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'createSharedResources must stay false' || { echo "FAIL: render failed for another reason than the shared-resources guard"; exit 1; }
	@echo "--> network policies: both flavors render the controller policies; llmisvc adds its own"
	@out=$$($(KSERVE) --set global.networkPolicy.enabled=true); \
	printf '%s' "$$out" | grep -q 'name: agent-platform-standalone-kserve-controller' || { echo "FAIL: kubernetes-flavor kserve controller policy missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'control-plane: kserve-controller-manager' || { echo "FAIL: controller selector missing"; exit 1; }; \
	printf '%s' "$$out" | grep -q 'kind: CiliumNetworkPolicy' && { echo "FAIL: CiliumNetworkPolicy under kubernetes flavor"; exit 1; }; true
	@out=$$($(LLMISVC) --set global.networkPolicy.enabled=true --set global.networkPolicy.flavor=cilium); \
	for pattern in 'name: agent-platform-standalone-kserve-controller' 'name: agent-platform-standalone-llmisvc-controller' '- kube-apiserver' 'control-plane: llmisvc-controller-manager'; do \
		printf '%s' "$$out" | grep -q -e "$$pattern" || { echo "FAIL: cilium-flavor kserve render lacks $$pattern"; exit 1; }; \
	done; \
	printf '%s' "$$out" | grep -q 'kind: NetworkPolicy' && { echo "FAIL: NetworkPolicy under cilium flavor"; exit 1; }; true
