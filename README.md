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

The templates in `helm/agent-platform-standalone/templates/` render the wiring
between the components: the public routes, the agentgateway data-plane
`Gateway`, network policies, the kagent routes and CRs, and the optional
CloudNativePG `Cluster`.

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

> **Helm version, current state.** The plain-CLI install is blocked on both
> Helm majors right now: Helm 3 cannot store the release — the dependency
> archives alone exceed the 1 MiB release-Secret cap
> ([#21](https://github.com/giantswarm/agent-platform-standalone/issues/21)) —
> and Helm 4's server-side apply fails on
> [giantswarm/agent-platform#250](https://github.com/giantswarm/agent-platform/issues/250).
> Helm 4 becomes the supported path once the #250 fix ships through a
> connectivity release and a pin bump; Helm 3 stays out. Installs through the
> Giant Swarm app platform (App CR — what the CI smoke runs) are unaffected.

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
[agentlab](https://github.com/giantswarm/agentplatform-kind) does on kind.

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
everything under `templates/` except `NOTES.txt` and `templates/backstage/`
are generated. Do not edit them.
The generator `hack/curate.sh` reads the fleet meta-package
`giantswarm/agent-platform` and the `agent-platform-connectivity` chart at the
version pinned in `curate.yaml`, and:

1. builds the dependency list from the fleet chart's component list, plus the
   extra dependencies declared in `curate.yaml` (`backstage`, `cloudnative-pg`);
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
   Backstage app-config and route);
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
make verify                  # what CI runs: go test, curate --check, render every example, schema and decision checks
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
