[![CircleCI](https://dl.circleci.com/status-badge/img/gh/giantswarm/agent-platform-standalone/tree/main.svg?style=svg)](https://dl.circleci.com/status-badge/redirect/gh/giantswarm/agent-platform-standalone/tree/main)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/giantswarm/agent-platform-standalone/badge)](https://securityscorecards.dev/viewer/?uri=github.com/giantswarm/agent-platform-standalone)

# agent-platform-standalone

One Helm chart that installs the Giant Swarm Agent Platform on any conformant
Kubernetes cluster with plain `helm install`. No GitOps controller is required.

The chart is a Helm umbrella. Each platform component is a dependency, keyed by
its exact chart name:

| Dependency | Default | What it is |
|---|---|---|
| `muster` | on | MCP gateway and OAuth front door |
| `valkey` | on | Session store for muster |
| `kagent` | off | Agent runtime (controller, UI) |
| `agentgateway` | off | Data-plane gateway in front of muster and kagent |
| `agent-platform-mcps` | off | The platform's MCP server registrations |
| `klaus-gateway` | off | Channel front door (Slack, CLI) |
| `dicebear` | on | Avatar service |
| `agent-sandbox` | off | Sandbox runtime for agents |
| `backstage` | off | Developer portal |
| `cloudnative-pg` | off | PostgreSQL operator |
| `model-manager` | off | Model management service (inventory, pull, load/unload, delete, kagent wiring), see [Model manager](#model-manager) |

One component has no dependency behind it: `modelServing` (off) renders the
KServe/vLLM model-serving layer itself, see [Model serving](#model-serving).

The templates in `helm/agent-platform-standalone/templates/` render the wiring
between the components: the public routes, the agentgateway data-plane
`Gateway`, network policies, the kagent routes and CRs, the optional
CloudNativePG `Cluster`, and the model-serving layer.

## Values layout

```yaml
global:                # the input contract, forwarded to every component chart
  domain: agents.example.com          # public hostnames derive from it
  identity:                           # the one login provider (OIDC)
    issuerUrl: https://dex.example.com
    clientId: agent-platform
    existingSecret: agent-platform-idp
  gatewayApi:
    parentRefs: []                    # the cluster's public Gateway
  networkPolicy: {enabled: false, flavor: kubernetes}
  observability:
    metrics: {serviceMonitor: {enabled: false}}
    traces: {otlp: {endpoint: ""}}
gatewayApi:
  gateway: {create: false}            # or: the chart's agentgateway Gateway is the edge
components:            # umbrella-owned: one entry per dependency
  kagent:
    enabled: true      # the Helm dependency condition
    controllerRoute:   # wiring the umbrella renders for kagent
      enabled: true
ingress:               # umbrella-owned wiring blocks
gateway:
kyvernoPolicies:
postgres:
kagent:                # the kagent chart's own values, forwarded verbatim
  kagent:              # nested: the Giant Swarm kagent chart vendors upstream as a subchart
    controller: {}
```

- `global.domain` is the one hostname input: `muster.`, `agentgateway.`,
  `kagent.`, `backstage.<domain>` by convention, each overridable on its
  component (`ingress.hostnames`, `components.kagent.controllerRoute.hostname`,
  `components.kagent.uiRoute.hostname`, `components.backstage.hostname`).
- Every public route attaches to the per-route override, else to the
  chart-owned edge when `gatewayApi.gateway.create` is true, else to
  `global.gatewayApi.parentRefs`. The render fails when none is set.
- `gatewayApi.gateway.create: true` adds an HTTPS listener for
  `*.<global.domain>` to the agentgateway Gateway (`gatewayApi.gateway.tls.secretName`
  names the wildcard certificate Secret); the public routes pin themselves to
  it via `sectionName`, so the plaintext listener never serves public hostnames.
  TLS and DNS stay outside the chart.
- `global.identity` is read by the umbrella templates (kagent JWT policy,
  Backstage app-config, kagent oauth2-proxy arguments). The muster chart still
  reads `muster.muster.oauth.server.*`; set the same values there, the render
  fails when they differ.
- Vanilla defaults: no network policy, no Kyverno object, no ServiceMonitor or
  PodMonitor, no VerticalPodAutoscaler, no OTLP endpoint — nothing that needs
  a CRD outside Kubernetes conformance. `examples/giantswarm.yaml` (generated)
  turns the fleet's defaults back on.
- `components.<name>.enabled` turns a dependency on or off. Hyphenated names
  need quotes on the command line: `--set 'components.klaus-gateway.enabled=true'`.
- `global`, `components`, `gatewayApi` and the wiring blocks (`ingress`,
  `gateway`, `kyvernoPolicies`, `extraObjects`, `postgres`) are validated
  strictly by `values.schema.json`. A component block is opaque here; the
  component chart validates it with its own schema.
- `kagent` and `agentgateway` values are nested one level (`kagent.kagent.*`,
  `agentgateway.agentgateway.*`) because the Giant Swarm charts vendor the
  upstream chart as a subchart. A later release flattens them.

### Backstage

`components.backstage.enabled: true` installs the Giant Swarm Backstage chart
as the vanilla UI. The umbrella renders its app-config from `global.*`: the
login provider `oidc-agent-platform` from `global.identity`, one installation
named after the release with `baseDomain: global.domain`, the in-cluster
Kubernetes entry and the muster installation, `app.extensions` limited to the
Agent Platform pages, and `/` redirects to `/agent-platform`. The route is
`backstage.<domain>`. The identity Secret must carry `backstage-session-secret`
and `dex-client-secret`. `components.backstage.extraScopes` defaults to the two
Dex cross-client scopes; set `[]` for a provider that rejects unknown scopes.
The agent create flow needs Flux on the cluster.

### Databases

kagent uses its bundled single-container Postgres by default (demo grade).
`components.cloudnative-pg.enabled: true` installs the CloudNativePG operator;
`postgres.enabled: true` renders the platform `Cluster`. The two need two
revisions: the operator chart templates its CRDs, so the `Cluster` has no API
to map to in the same first install. Install with the operator on, then
`helm upgrade --set postgres.enabled=true`. The `Cluster` carries
`helm.sh/resource-policy: keep`.

Backstage keeps its catalog and sessions in in-memory SQLite by default, lost
on every pod restart. With the CloudNativePG operator on the cluster, set
`backstage.database.engine: postgresql`: the Backstage chart renders its own
CNPG `Cluster` (`backstage-cnpg`) and this chart's app-config overlay points
the backend at it (pg client, schema division mode).

### Model serving

`components.modelServing.enabled: true` adds the layer that serves local
models on [KServe](https://kserve.github.io/website/) with
[vLLM](https://docs.vllm.ai/), for installs with GPU nodes. Off by default and
inert while off. The component is rendered by the umbrella itself (no Helm
dependency; `templates/model-serving/`, `files/model-serving/`), and follows
decision D1 of the Model Manager design: integrate KServe, do not bundle it.

**Prerequisite: KServe.** The chart installs neither the KServe CRDs nor the
controller. With the component on, the render fails until the
`serving.kserve.io/v1alpha1` and `v1beta1` APIs exist on the cluster
(`components.modelServing.kserve.requireApi`); an offline `helm template`
passes the check with `--api-versions serving.kserve.io/v1alpha1
--api-versions serving.kserve.io/v1beta1`. Install KServe first, for example
in raw-deployment mode (no Knative, no Istio), which is what the GPU
reference install runs:

```sh
helm install kserve-crd oci://ghcr.io/kserve/charts/kserve-crd --version v0.20.0 -n kserve --create-namespace
helm install kserve oci://ghcr.io/kserve/charts/kserve --version v0.20.0 -n kserve \
  --set kserve.controller.deploymentMode=RawDeployment
```

plus the NVIDIA device plugin (or GPU operator) so nodes expose
`nvidia.com/gpu`. The portal's Serving view lights up on the CRDs; the chart's
own state check is the API guard above.

What the component renders, all under `components.modelServing`:

| Object | Where | Key |
|---|---|---|
| `ClusterServingRuntime` (vLLM) | cluster | `runtime.*`: image (`docker.io/vllm/vllm-openai`, pinned; installs with other hardware point it at their own build), common args, env, resource envelope, `/dev/shm` size, startup budget, node selector, supported model formats |
| `Namespace` | `namespace.name` (`model-serving`) | `namespace.create`, `namespace.labels` (e.g. a Pod Security level: the upstream vLLM image runs as root) |
| serving presets (`ConfigMap` per preset) | release namespace | `presets`, `shippedPresets` — see below |
| discovery `ConfigMap` `agent-platform-model-serving` | release namespace | `serving.*` defaults (GPU resource name, RuntimeClass, node selector, deployment strategy, route timeout) |
| HF cache `PersistentVolumeClaim` | serving namespace | `cache.pvc.*` (name, size, class, existing claim, static PV); kept on uninstall |
| Kyverno `ClusterPolicy` x2 | cluster | `policies.*`; needs Kyverno, off by default |
| network policies (`NetworkPolicy` or `CiliumNetworkPolicy`) | serving namespace, plus the kagent namespace (cilium) | `networkPolicy.*`, rendered under `global.networkPolicy` — see [Network policies](#network-policies-1) |

The chart creates no `InferenceService`: the portal's serve flow (and
model-manager) do, from a preset. The `ClusterServingRuntime` and the presets
are what makes a served model a platform artifact instead of a hand-written
manifest.

#### Serving presets

A preset is a reviewed recipe for one model — the flags, template and memory
numbers that took someone a night to get right. The chart ships a set under
`files/model-serving/presets/` (seeded from the GPU reference install:
Qwen3.8 27B with speculative decoding and a patched chat template, Qwen3.5
27B and 35B-A3B, Qwen3 14B, Qwen3 Coder Next, Devstral Small 2, Nemotron 3
Super) and publishes every preset in effect as a `ConfigMap` in the release
namespace:

```sh
kubectl -n agent-platform get configmap -l agent-platform.giantswarm.io/serving-preset=true
kubectl -n agent-platform get configmap agent-platform-serving-preset-qwen3-14b -o jsonpath='{.data.preset\.yaml}'
```

This is the contract the portal codes against (giantswarm/backstage#2193):

- **Selector**: label `agent-platform.giantswarm.io/serving-preset=true`,
  release namespace. Each ConfigMap is named `agent-platform-serving-preset-<name>`
  and also carries `agent-platform.giantswarm.io/preset: <name>` and
  `agent-platform.giantswarm.io/preset-source: shipped|values`.
- **Payload**: key `preset.yaml`, one `ServingPreset` document
  (`apiVersion: agent-platform.giantswarm.io/v1alpha1`). The JSON schema is
  `helm/agent-platform-standalone/files/model-serving/serving-preset.schema.json`
  — also the schema of `components.modelServing.presets[]` in
  `values.schema.json`, and what `go test ./hack/presets` and
  `make verify-model-serving` validate every shipped and published preset
  against.
- **Discovery**: ConfigMap `agent-platform-model-serving` (label
  `agent-platform.giantswarm.io/model-serving-config=true`, key
  `config.yaml`, kind `ModelServingConfig`): the serving namespace, the
  default runtime, `gpuResourceName`, `runtimeClassName`, `nodeSelector`,
  `deploymentStrategyType`, `timeoutSeconds`, the cache claim (`cache.claimName`,
  `cache.mountPath`, whether the Kyverno redirect is on) and the preset
  selector plus names.

```yaml
apiVersion: agent-platform.giantswarm.io/v1alpha1
kind: ServingPreset
metadata:
  name: qwen3-14b                 # DNS-1123 label, <= 30 chars; default InferenceService name
spec:
  displayName: Qwen3 14B          # required
  description: |                  # notes for the operator (hardware the recipe was tuned on, limits)
  model:
    id: Qwen/Qwen3-14B            # required: Hugging Face repository
    storageUri: hf://Qwen/Qwen3-14B   # required: predictor.model.storageUri (hf://, pvc://, s3://, ...)
    format: vLLM                  # modelFormat.name; published presets always carry it
    contextLength: 8192           # informational
    capabilities: [chat, tools]   # informational tags
    license: apache-2.0
  runtime: kserve-vllm            # ClusterServingRuntime; empty = components.modelServing.runtime.name
  args: [--max-model-len=8192]    # vLLM flags, complete and literal (never --chat-template)
  env: []                         # extra EnvVars of the runtime container
  chatTemplate:                   # optional; authoring: one of file | content | existingConfigMap
    configMap: agent-platform-chat-template-qwen3-14b   # published: the ConfigMap in the serving namespace
    key: chat-template.jinja
    mountPath: /mnt/chat-template # args then end with --chat-template=<mountPath>/<key>
  resources:
    gpus: 1                       # requested as serving.gpuResourceName
    requests: {cpu: "4", memory: 48Gi}
    limits: {cpu: "8", memory: 64Gi}
  requirements:
    weightsGiB: 28                # required
    overheadGiB: 30               # KV cache, activations, runtime; fit check = weights + overhead
  scheduling:
    nodeSelector: {}              # merged over serving.nodeSelector
  predictor: {}                   # extra InferenceService predictor fields, verbatim
```

Composing the `InferenceService` from a published preset is mechanical:
`metadata.name` from the preset (or the user's choice), the discovery
ConfigMap's namespace, `spec.predictor.model` = `{modelFormat: {name:
spec.model.format}, runtime: spec.runtime, storageUri: spec.model.storageUri,
args: spec.args, env: spec.env, resources: spec.resources with
<gpuResourceName>: gpus in requests and limits}`, `nodeSelector` =
discovery default merged with `spec.scheduling.nodeSelector`,
`runtimeClassName`, `deploymentStrategy.type` and `timeout` from discovery,
`spec.predictor` copied on top, and — when `spec.chatTemplate` is set — the
ConfigMap mounted as a volume at `mountPath`. Label the InferenceService
`agent-platform.giantswarm.io/preset: <name>` so the view can show where it
came from.

Installs add presets under `components.modelServing.presets` (same document
shape; a chat template as inline `content` or a pre-created
`existingConfigMap`), replace a shipped one by reusing its name, drop the
shipped set with `shippedPresets.enabled: false` or single ones with
`shippedPresets.exclude`. The render fails on a preset with a bad name,
without `displayName`, `model.id`, `model.storageUri` or
`requirements.weightsGiB`, or with an unresolvable chat template.

#### Model cache

Every preset's `hf://` download goes through the KServe storage-initializer
into an ephemeral volume — on every restart, hours for a large model. The
component renders one `PersistentVolumeClaim` (`cache.pvc`, default 500 Gi;
`existingClaim` for a pre-provisioned one, `storageClassName: "-"` plus
`volumeName` for a static local PV on the GPU node) with one subdirectory per
InferenceService. With Kyverno on the cluster and
`components.modelServing.policies.enabled: true`, two `ClusterPolicy` objects
wire every predictor pod in the serving namespace to it at admission: an init
container ahead of the storage-initializer creates the pod's subdirectory
world-writable (the kubelet would create a missing subPath directory root-owned
`0750`, which the storage-initializer's uid cannot write — the step that used
to need someone with node access), the storage-initializer's and the runtime's
`/mnt/models` are redirected into it, the storage-initializer gets a memory
limit that survives multi-GB downloads (`policies.storageInitializerMemoryLimit`),
and the Deployment a `progressDeadlineSeconds` long enough for a first download
(`policies.progressDeadlineSeconds`). No InferenceService change, no manual node
or namespace preparation. Without Kyverno the PVC is still created and published
(model-manager's pre-warm downloads and `pvc://` presets use it), but `hf://`
presets download on every start.

#### Network policies

With `global.networkPolicy.enabled: true` the component renders the policies
of the serving namespace in the selected flavor, so a served model needs no
hand-written rule; the discovery ConfigMap publishes
`spec.networkPolicy.{enabled,flavor}` so the portal can say what the
installation has. Both flavors:

- **Ingress to the predictor pods** (every pod KServe runs for an
  InferenceService, label `serving.kserve.io/inferenceservice`) on the
  runtime port (`networkPolicy.predictor.port`, 8080) from the kagent agent
  pods (`app: kagent` in the kagent namespace — the auto-created ModelConfig
  dials the predictor Service, translated to the pod port before policy
  evaluation), from the release namespace (Backstage, model-manager, the
  agentgateway data plane) and from
  `networkPolicy.predictor.additionalIngressNamespaces` (the KServe ingress
  Gateway's namespace when InferenceServices are exposed through it,
  Prometheus' for KServe's scrape annotations).
- **Egress to the model download endpoints** on 443, plus DNS, from the
  predictor pods (the storage-initializer's `hf://` download and the
  runtime) and from model-manager's pre-warm download Jobs
  (`model-manager.giantswarm.io/component=download`); on the `kserve`
  backend model-manager's own Hub calls get the same egress
  (`templates/model-manager/netpol.yaml`).

The download endpoints are where the flavors differ; the values comments
carry the same trade-off:

- **cilium**: `networkPolicy.huggingFace.fqdns` are Cilium FQDN selectors
  (`matchName` / `matchPattern`; the defaults cover `huggingface.co` and the
  LFS / Xet download fronts under `hf.co`), enforced through the DNS proxy
  clause (`rules.dns`) the policies carry on their DNS egress — without it
  Cilium never learns the resolved addresses and a `toFQDNs` rule matches
  nothing, which is why hand-written policies used to fall back to
  `world:443`. Needs Cilium's L7 proxy (on by default). The cilium flavor
  also admits the kubelet's probes on the runtime port from the `host` and
  `remote-node` entities — under `policyEnforcementMode: always` an
  egress-only policy default-denies them and the predictor is killed before
  it turns Ready — and renders the agent-side egress from `app: kagent` pods
  to the predictor pods (the chart's agent egress covers 443 only).
- **kubernetes**: vanilla NetworkPolicy selects IP blocks, never names. With
  `networkPolicy.huggingFace.cidrs` empty (the default) the egress admits
  every public destination on 443 (`0.0.0.0/0` minus
  `global.networkPolicy.kubernetes.worldExcludedCIDRs`, the data plane's own
  rule), because Hugging Face's download fronts are CDNs whose addresses
  change and a static list would break downloads silently; a CIDR list
  narrows it to a mirror or proxy. The kubelet's probes are not subject to
  NetworkPolicy, and the agents' egress is unrestricted in this flavor (the
  chart renders no egress policy for agent pods), so no agent-side rule is
  rendered.

`global.networkPolicy.additionalEgressCIDRs` / `additionalEgressFQDNs` apply
to both (an S3 endpoint for `s3://` presets). The render fails on an FQDN
entry that is not exactly one of `matchName` / `matchPattern`, a value that
is not a CIDR, a namespace that is not a DNS label, or a port out of range.

### Model manager

`components.model-manager.enabled: true` installs
[model-manager](https://github.com/giantswarm/model-manager), the service
behind the portal's model management: model inventory (downloaded and
loaded), pull with progress, load/unload, delete and automatic kagent
`ModelConfig` wiring, as one REST API and as MCP tools. Off by default and
inert while off. It is a Helm dependency (chart `model-manager`, pinned in
`curate.yaml`); the umbrella renders the wiring around it from
`templates/model-manager/`.

The service's own configuration is the model-manager chart's, under the
`model-manager:` block — the umbrella cannot compute a dependency's values at
render time, so the backend is selected there, like every other component's
values:

```yaml
components:
  model-manager:
    enabled: true
    route:                       # the API on the agentgateway data plane
      enabled: true
      pathPrefix: /model-manager # https://agentgateway.<domain>/model-manager
      jwtAuthentication:
        enabled: true            # no Dex token, no API (401 at the gateway)
model-manager:
  backend: ollama                # ollama | kserve
  ollama:
    endpoint: http://172.21.0.1:11434   # REQUIRED for ollama: Ollama as pods reach it
gateway:
  jwksEgress:
    enabled: true                # the data plane fetches the JWKS from Dex
```

- **Backends.** `ollama` proxies a host Ollama — the laptop / agentlab dev
  loop (the Ollama backend ADR); `ollama.endpoint` is required (on kind the
  docker network gateway; `ollama.agentHost` when agent pods dial a different
  address). `kserve` manages `InferenceService`s on the
  [Model serving](#model-serving) component's KServe: pull = a pre-warm
  download Job into the HF cache, load = an `InferenceService` from a
  serving preset, plus fit checks, node inventory and Hugging Face search.
  The render fails without the `serving.kserve.io/v1beta1` API unless
  `components.modelServing` is on (or
  `components.model-manager.kserve.requireApi: false`). The two components
  describe one serving layer without duplicate config: model-manager reads
  the runtime, GPU resource name, cache claim and preset selector from the
  modelServing **discovery ConfigMap** at run time
  (`model-manager.kserve.discovery.configMap: agent-platform-model-serving`,
  the chart default); only `model-manager.kserve.namespace` is static and
  must equal `components.modelServing.namespace.name` (both default
  `model-serving`). A `kserve.*` override that disagrees with
  `components.modelServing` (runtime, GPU resource, cache claim, preset
  namespace or selector, discovery ConfigMap) fails the render. An unknown
  backend fails the render.
- **Route.** `components.model-manager.route` mirrors the kagent
  `controllerRoute`: an `AgentgatewayBackend` for the Service, an `HTTPRoute`
  at `pathPrefix` on the agentgateway Gateway with the prefix stripped
  (model-manager sees `/api/v1/...`), and the public `HTTPRoute` on the outer
  Gateway when the chart-owned edge is not the data plane. The hostname is
  `agentgateway.<global.domain>` (`route.hostname` overrides). Requires an
  `agentgateway-*` `ingress.mode`.
- **Identity boundary.** `route.jwtAuthentication` renders the
  `AgentgatewayPolicy` that validates the bearer JWT (signature, issuer,
  expiry) against `global.identity.issuerUrl` before forwarding — without a
  token the gateway answers 401. model-manager checks no identity itself;
  the gateway policy is the boundary, the same trust model as the kagent
  controller route. The JWKS is fetched through a static
  `AgentgatewayBackend` (`jwks.host/port/path`, the in-cluster Dex by
  default; `gateway.jwksEgress` must be on). An issuer that serves its JWKS
  over TLS (a Dex with `web.https`, e.g. the lab's) sets `jwks.tls.enabled`
  — the data plane then verifies against `jwks.tls.caSecretName` (key
  `ca.crt`), defaulting to `global.identity.ca.secretName`.
- **Portal.** With the route on, the Backstage app-config gets
  `agentPlatform.modelManager.installations.<release>.apiBaseUrl:
  https://<hostname><pathPrefix>` next to the kagent entry; the portal
  backend forwards the user's Dex ID token to it.
- **MCP.** The model-manager chart registers its MCP endpoint with muster
  through its own `MCPServer` CR (`model-manager.muster.mcpServer.enabled`,
  on by default; the muster component must be on): tools appear as
  `x_model-manager_<tool>` (`list_models`, `pull_model`, `load_model`,
  `unload_model`, `delete_model`, `wire_model`, `get_job`, and on the
  kserve backend `list_presets`, `search_models`, `check_fit`,
  `list_nodes`). muster reaches it in-cluster, not through the route.
- **Wiring.** ModelConfigs land in `model-manager.kagent.namespace`, which
  must be the kagent component's namespace (`kagent.kagent.namespaceOverride`,
  `kagent` by default); an install without kagent sets
  `model-manager.kagent.disableWiring: true`. The Ollama backend wires
  models with kagent's native keyless `Ollama` provider.
- **Network policies** (`global.networkPolicy`, both flavors): ingress to
  model-manager from the agentgateway data plane, muster and Backstage on the
  Service port; egress to DNS, the Kubernetes API and — for the ollama
  backend — the endpoint (an IP endpoint gets a `/32`; a hostname opens the
  port to every destination, since neither flavor resolves names here); the
  data plane's and muster's egress to model-manager; on the `kserve`
  backend, egress to the Hugging Face Hub from the modelServing component's
  endpoint list (`components.modelServing.networkPolicy.huggingFace`). The
  model-manager chart's own NetworkPolicy stays off.

`make verify-model-manager` renders the component off (nothing), on (the
service, MCPServer, route, JWT policy, app-config entry, both network-policy
flavors) and checks every guard above.

## Install

```sh
helm dependency build helm/agent-platform-standalone
helm install agent-platform helm/agent-platform-standalone \
  --namespace agent-platform --create-namespace \
  -f examples/kind-lab-dex.yaml
```

`examples/kind-lab-dex.yaml` is a kind quick start: the chart's agentgateway
Gateway is the public edge and a lab Dex is the identity provider (see the
walkthrough below). `examples/giantswarm.yaml` (generated) is what a Giant
Swarm installation sets on top of the vanilla defaults.

> **Helm version.** Installing with the Helm CLI requires **Helm 4**. Helm 3
> cannot store the release: the dependency archives alone exceed the 1 MiB
> release-Secret cap
> ([#21](https://github.com/giantswarm/agent-platform-standalone/issues/21)),
> independent of which components are enabled — that holds until the
> dependency payload shrinks below ~750 KiB (`kagent` dominates). Helm 4's
> server-side apply used to reject the chart on
> [giantswarm/agent-platform#250](https://github.com/giantswarm/agent-platform/issues/250);
> fixed since the chart curates agent-platform 3.2.2. Installs through the
> Giant Swarm app platform (App CR — what the CI smoke runs) are unaffected
> and work under either constraint. Rendering, linting and
> `helm dependency build` work on both majors.

### Lab quick start (kind)

> **LAB ONLY — never a production path.** `prerequisites/lab-dex.yaml`
> deploys Dex with static password users and fixed, world-readable secrets,
> mints a self-signed wildcard certificate, and replaces the kind cluster's
> CoreDNS Corefile. Use it on a throwaway kind cluster and nowhere else.

The prerequisites manifest provides everything `examples/kind-lab-dex.yaml`
expects: the lab Dex (static user `admin@example.com` / `password`), the
`agent-platform-idp` credentials Secret, the wildcard certificate Secret
`agent-platform-tls` and the lab CA (`agent-platform-idp-ca`, as Secret and
ConfigMap) for muster (`extraCaFile`), Backstage (`NODE_EXTRA_CA_CERTS`) and
the kagent oauth2-proxy (`--provider-ca-file`), plus a CoreDNS rewrite so
`*.127.0.0.1.nip.io` resolves to the edge Gateway inside pods. The exact
order:

```sh
kind create cluster
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml
kubectl apply -f prerequisites/lab-dex.yaml
kubectl -n agent-platform wait --for=condition=complete job/lab-dex-cert-gen
kubectl -n kube-system rollout restart deployment coredns   # pick the rewrite up immediately
helm dependency build helm/agent-platform-standalone
helm install agent-platform helm/agent-platform-standalone \
  --namespace agent-platform --create-namespace \
  -f examples/kind-lab-dex.yaml
```

muster restarts until the edge Gateway and the lab Dex answer its OIDC
discovery — that settles itself. Backstage does not: its login-provider
check probes the Dex issuer for only ~15 seconds at startup and a failed
backend keeps serving readiness 503 without exiting, so when Backstage boots
before the edge Gateway on a fresh install it stays NotReady until restarted
(the CI smoke does the same):

```sh
kubectl -n agent-platform rollout restart deployment backstage
```

To use the platform from the host, forward
the edge (port 443 needs root; outside the cluster the public nip.io wildcard
already resolves `*.127.0.0.1.nip.io` to localhost):

```sh
sudo kubectl -n agent-platform port-forward service/agentgateway 443:443
```

then log in at `https://backstage.127.0.0.1.nip.io` or point an MCP client at
`https://muster.127.0.0.1.nip.io/mcp` (`admin@example.com` / `password`). The
certificate is signed by the lab CA; export it with

```sh
kubectl -n agent-platform get secret agent-platform-idp-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > lab-ca.crt
```

and trust it in the browser or pass it to the client, or click through the
browser warning — it is a lab.

One lab-topology limitation to know before chasing it as a bug: the Backstage
pages that read through the Kubernetes API with the signed-in user's token —
the Agents list, the MCPServer fleet health — show "Couldn't read 1
installation" on plain kind, because the kind apiserver trusts no OIDC issuer.
The muster-backed pages (MCP Servers dashboard, Tool explorer, Sessions) work.
On a cluster whose apiserver trusts the same issuer those reads work too;
wiring the apiserver to the lab Dex is exactly what
[agentlab](https://github.com/giantswarm/agentlab) does on kind.

### Upgrades

`helm upgrade` never touches the `crds/` directories of the chart or its
dependencies, so apply the candidate's CRDs first — one line, prints the
dependency CRDs too (including those of disabled dependencies, harmless):

```sh
helm show crds helm/agent-platform-standalone | kubectl apply --server-side -f -
```

The CI upgrade test runs exactly this step between the last published chart
and the release candidate (`tests/ats/upgrade-hook.sh`).

## The chart is generated

`Chart.yaml`, `Chart.lock`, `values.yaml`, `examples/giantswarm.yaml` and
everything under `templates/` except the files `curate.yaml` lists in
`templates.extra` (`NOTES.txt`, `templates/backstage/`,
`templates/model-serving/`, `templates/model-manager/`, a few single files)
are generated. Do not edit them. `files/` (the shipped serving presets, chat templates and the preset
schema) is hand-authored.
The generator `hack/curate.sh` reads the fleet meta-package
`giantswarm/agent-platform` and the `agent-platform-connectivity` chart at the
version pinned in `curate.yaml`, and:

1. builds the dependency list from the fleet chart's component list, plus the
   extra dependencies declared in `curate.yaml` (`backstage`, `cloudnative-pg`,
   `model-manager`);
2. resolves each major-bounded range (`5.x`) to the newest published version and
   writes that exact pin into `Chart.yaml`;
3. transforms the fleet values into the layout above: fleet-only keys dropped,
   each toggle default taken from the fleet's own `components.<name>.enabled`,
   umbrella wiring lifted out of the component blocks, `networkPolicy` moved
   under `global` (Helm forwards only `global.*` to subcharts), blocks renamed
   to the chart name and nested under the wrapper's subchart key;
4. merges `overlay/contract.yaml` (the umbrella's input contract: identity
   defaults, the Backstage inputs, the wiring the umbrella templates need
   inside component blocks — kept by every install), then
   `overlay/vanilla.yaml` (the fleet defaults a vanilla cluster turns off; a
   leaf under an umbrella-owned key must override a path the fleet-derived
   values carry, so a fleet rename fails the run instead of silently turning a
   default back on);
5. writes `examples/giantswarm.yaml`: every leaf of `overlay/vanilla.yaml`
   restored to its fleet value, plus the inputs of `overlay/giantswarm.yaml`;
6. copies the connectivity chart's templates, with the helper names prefixed by
   the chart name and every values path the transform moved rewritten from the
   same rules (`.Values.networkPolicy` becomes `.Values.global.networkPolicy`).
   A template the connectivity chart drops is deleted here; `templates.extra`
   in `curate.yaml` names the files this chart owns itself (`NOTES.txt`, the
   Backstage app-config and route, the model-serving templates). A component
   without a dependency (`umbrellaComponents`, today `modelServing`) gets its
   whole `components.<name>` block from `overlay/contract.yaml`, `enabled`
   included; the generator admits the name into the components map and
   requires the toggle;
7. runs `helm dependency update` to refresh `Chart.lock`, and keeps the committed
   file when the pins did not change.

The transform is deny-unknown. Every top-level key of the fleet and
connectivity values must have a rule in `curate.yaml`; a fleet rename or a new
Giant Swarm-only default fails the run instead of leaking into the defaults. A
component values block that carries a top-level `enabled` fails too: since
`agent-platform` 3.0.0 the fleet's `components.<name>.enabled` is the single
on/off switch, and a second toggle would be read by nothing.
Running the generator twice yields no diff.

`values.schema.json` is generated from `values.yaml` with the `helm schema`
plugin (losisin/helm-values-schema-json), the standard Giant Swarm pre-commit
hook. The generator keeps the `# @schema` annotations for it: every component
block carries `skipProperties: true; additionalProperties: true`, so the schema
is strict for the umbrella-owned keys only.

```sh
make curate                  # regenerate Chart.yaml, values.yaml, Chart.lock, templates, examples/giantswarm.yaml
pre-commit run --all-files   # regenerate values.schema.json and the chart README
make verify                  # what CI runs: go test, curate --check, render every example, schema, decision, model-serving and model-manager checks
```

Requirements: Go 1.26, Helm 3.8 or newer. No registry login is needed; the
charts are public. If Helm returns `401 unauthorized` for `gsoci.azurecr.io`,
a stale login is the cause: run `helm registry logout gsoci.azurecr.io` (or
`docker logout gsoci.azurecr.io`).

## CI

- `build-chart` (generated): app-build-suite lint, template validation with
  `helm/agent-platform-standalone/ci/ci-values.yaml`, schema validation.
- `verify` (`.circleci/custom.yml`): `make verify`. `hack/curate.sh --check`
  validates the committed pins and never asks the registry for newer versions,
  so a component release does not fail an unrelated PR. CI runs
  `helm dependency build`, never `update`; a `Chart.yaml` that no longer matches
  `Chart.lock` fails the build. `verify-render` renders every `examples/*.yaml`.
  `verify-model-serving` renders the modelServing component off (nothing) and
  on (runtime, presets, cache, policies, the network policies in both
  flavors and their guards) and validates every published preset
  against `files/model-serving/serving-preset.schema.json` with
  `hack/presets`; `go test ./hack/presets` validates the shipped preset files.
  `verify-model-manager` renders the model-manager component off (nothing) and
  on (service, MCPServer, route, JWT policy, app-config entry, network
  policies) and checks its render-time guards.
- `execute-chart-tests` (generated, app-test-suite on kind): the install and
  auth smoke plus the upgrade test (`tests/ats/test_smoke.py`, configured by
  `.ats/main.yaml`). The smoke applies the Gateway API CRDs and
  `prerequisites/lab-dex.yaml`, installs the candidate with
  `examples/kind-lab-dex.yaml`, and asserts: every Deployment Ready, an
  unauthenticated `/mcp` answering 401 with the `WWW-Authenticate` discovery
  chain, a lab Dex static-user login reaching `/mcp` with 200, and a kagent
  `Agent` reaching Ready against a fake model provider. The upgrade scenario
  installs the last published chart, applies the candidate's CRDs (the
  documented one-liner, `tests/ats/upgrade-hook.sh`), upgrades, and re-runs
  the readiness and auth assertions.
- Renovate does not touch chart dependencies. The generator owns the BOM.

The templates and component values are regenerated from the fleet charts on
every `make curate` run and byte-checked by `hack/curate.sh --check`, so
neither the wiring nor the defaults can drift. `make verify-decisions` then
asserts the rendered objects: the vanilla defaults render zero policy and
monitoring objects, hostnames derive from `global.domain`, the generated Giant
Swarm example turns the fleet defaults back on, and the guards fail loudly.
