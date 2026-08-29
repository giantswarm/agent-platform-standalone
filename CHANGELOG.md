# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `hack/curate.sh`, the generator that produces `Chart.yaml`, `Chart.lock` and `values.yaml` from the `agent-platform` fleet chart through a deny-unknown transform configured in `curate.yaml` and `overlay/vanilla.yaml`.
- Umbrella chart with one dependency per component (`muster`, `valkey`, `kagent`, `agentgateway`, `agent-platform-mcps`, `klaus-gateway`, `dicebear`, `agent-sandbox`, `backstage`, `cloudnative-pg`), toggled by `components.<name>.enabled`, pinned to exact versions.
- Wiring templates (routes, agentgateway data-plane Gateway, network policies, kagent CRs, CloudNativePG Cluster) generated from `agent-platform-connectivity`: helper names take the chart-name prefix, and every values path the transform moves is rewritten from the same rules. A template that reads a key this chart does not carry fails the run, and `--check` fails on a hand edit.
- `values.schema.json`, strict for the umbrella-owned keys, opaque for component blocks.
- `examples/kind-lab-dex.yaml` and the `verify` CI job (`make verify`).
- `templates.patch` (exact-once text edits on generated templates, a stale patch fails the run) and `annotations` (schema `# @schema` line comments injected into the generated values) in `curate.yaml`; `allowShadow` declarations so a key copied from one source chart fails the run when the other source's shadow copy is not discarded deliberately.
- Schema admits the real Gateway API listener and core `EnvVar` item shapes (`itemRef`), extra `parentRef`/`podSecurityContext` fields, and validates `networkPolicy.flavor` as an enum.
- `validateIngress` fails loudly in `muster-direct` mode on anything that renders `agentgateway.dev` resources (per-server MCP backends, `components.kagent.controllerRoute`, `klaus-gateway.agentgatewayRoute`), whose CRDs that mode never installs.
- The `global.*` input contract, curated from `agent-platform` 3.1.0: `global.domain` (public hostnames derive from it, each overridable per component), `global.identity` (`issuerUrl`, `clientId`, `existingSecret`, plus the umbrella-only `ca`), `global.gatewayApi.parentRefs`, `global.networkPolicy` (moved from `networkPolicy`; the template rewrite follows), `global.observability.metrics.serviceMonitor` and `global.observability.traces.otlp`.
- `gatewayApi.gateway.create` (upstream, curated): the chart-owned agentgateway Gateway becomes the public edge with an HTTPS listener for `*.<global.domain>` (`tls.secretName`, `serviceType`); the public routes pin themselves to that listener via `sectionName` and the layer-1 routes to the agentgateway Service are not rendered in that mode.
- Vanilla defaults (`overlay/vanilla.yaml`): network policies off (`kubernetes` flavor when on), `kyvernoPolicies` off, ServiceMonitors and PodMonitors off, no OTLP endpoint, dicebear off, agent-sandbox pod-security policy off.
- `overlay/contract.yaml` (the umbrella's input contract, never reverted) next to `overlay/vanilla.yaml`; `examples/giantswarm.yaml` is generated from the vanilla overlay and `overlay/giantswarm.yaml`. A vanilla-overlay leaf under an umbrella-owned key must override a path the fleet-derived values carry.
- `ingress.httpRoute.timeouts` (`request: 0s`) on the umbrella-owned routes; the Envoy `BackendTrafficPolicy` stays opt-in.
- `components.backstage`: the umbrella renders the Backstage app-config ConfigMap (login provider from `global.identity`, one installation named after the release, the in-cluster Kubernetes and muster entries, `app.extensions` limited to the Agent Platform pages, `app.rootRedirect: /agent-platform`) and the `backstage.<domain>` route; `hostname`, `parentRefs`, `extraScopes`, `disabledExtensions`, `skillsRepositories` inputs.
- `make verify-decisions`: render assertions for the vanilla defaults, the derived hostnames, the Giant Swarm example, the network-policy flavors, the edge Gateway and the guards.
- A render-time guard fails the install when `components.kagent.uiRoute.hostname` is overridden while the oauth2-proxy `redirect-url` still derives from `global.domain` — the callback would land on a hostname the route no longer serves.
- The CloudNativePG `Cluster` carries `helm.sh/resource-policy: keep` (upstream, curated); its PodMonitor follows `global.observability.metrics.serviceMonitor`.
- `prerequisites/lab-dex.yaml`: a lab-only OIDC provider for kind quick starts and CI — Dex with static password users, the `agent-platform` client plus the `dex-k8s-authenticator` trusted-peers client, the `agent-platform-idp` credentials Secret, an in-cluster Job minting the self-signed wildcard certificate and CA, an HTTPRoute publishing Dex on the chart-owned edge Gateway, and a CoreDNS rewrite of `*.127.0.0.1.nip.io` to the edge Gateway Service. The README's "Lab quick start (kind)" documents the apply/install order.
- The ATS kind smoke and upgrade tests run on every PR (`.ats/main.yaml`, `tests/ats/test_smoke.py`): prerequisites, candidate install with `examples/kind-lab-dex.yaml`, every Deployment Ready, unauthenticated `/mcp` answering 401 with the `WWW-Authenticate` discovery chain, a lab Dex static-user login (dynamic client registration, authorization code + PKCE, the Dex form) reaching `/mcp` with 200, a kagent `Agent` Ready against a fake model provider, and an upgrade from the last published chart including the documented CRD re-apply one-liner (`tests/ats/upgrade-hook.sh`).

### Changed

- Curated against `agent-platform` 3.1.0, whose connectivity chart reads the `global.*` contract with backward-compatible fallbacks; the hostname derivation, parentRefs fallback, route timeouts, monitor/OTLP gating, JWT-issuer default and edge mode are upstream template behavior now, not downstream patches.
- `networkPolicy` is `global.networkPolicy`.
- Route label and annotation maps under `ingress.httpRoute` and `ingress.backendTrafficPolicy` accept any key (the schema closed them).
- `gateway.parameters.dataPlaneEnv` defaults to `[]`: OTEL exporter env comes from `global.observability.traces.otlp`, the list is for extra data-plane env only.

### Fixed

- The legacy-toggle guard probes `klaus-gateway.enabled` by value, not presence: it fires on an explicit `false` in either component state and ignores `true`. The klaus-gateway chart's own `enabled: true` default reaches `.Values` through Helm's coalescing while the dependency is on, and through chart-operator's coalesced override values even while it is off (the chart installed as a Giant Swarm App — caught by the ATS kind smoke, where the presence probe failed every install), so only an explicit `false` is provably the operator's; `verify-decisions` covers all three cases.
- Backstage runs with its base configuration again: the backstage chart passes `extraAppConfig` as `--config` flags and any explicit `--config` replaces Backstage's default config set, so the backend ran on the umbrella's overlay alone and the app plugin failed startup on schema keys only the image's base config carries. `overlay/contract.yaml` restates the image's own base flags ahead of the overlay.
- `examples/kind-lab-dex.yaml` names the valkey password key for the default ACL user (the chart's fallback key does not exist in `agent-platform-idp`), trusts the `dex-k8s-authenticator` audience on tokens Backstage forwards to muster, and sizes the fat components for a two-core lab VM.
- The kagent UI route's direct backendRef honors `kagent.kagent.fullnameOverride` instead of targeting a Service named after the release.
- The public muster `/` route and the kagent Namespace render only when their components are enabled.
- Validation messages, `required` errors and values comments name the keys of this chart's layout (`components.kagent.*`, `kagent.kagent.*`) instead of the fleet paths nothing here reads; the legacy-toggle hints probe the renamed `klaus-gateway`/`agent-sandbox` keys and no longer point at a nonexistent UPGRADE.md.
- `NOTES.txt` no longer aborts the render when `gateway.listeners` is empty.
- A `--check` failure's `*.curate` diff sidecars no longer mask the real diff on the next run; write runs clean them up.
- The overlay can no longer reintroduce a component-level `enabled` toggle that nothing reads.
- `renovate.json5` extends `renovate-custom.json5`, so the helmv3/helm-values manager disables actually apply before the next devctl regeneration.

[Unreleased]: https://github.com/giantswarm/agent-platform-standalone/tree/main
