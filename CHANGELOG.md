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

### Changed

- Curated against `agent-platform` 3.1.0, whose connectivity chart reads the `global.*` contract with backward-compatible fallbacks; the hostname derivation, parentRefs fallback, route timeouts, monitor/OTLP gating, JWT-issuer default and edge mode are upstream template behavior now, not downstream patches.
- `networkPolicy` is `global.networkPolicy`.
- Route label and annotation maps under `ingress.httpRoute` and `ingress.backendTrafficPolicy` accept any key (the schema closed them).
- `gateway.parameters.dataPlaneEnv` defaults to `[]`: OTEL exporter env comes from `global.observability.traces.otlp`, the list is for extra data-plane env only.

### Fixed

- The kagent UI route's direct backendRef honors `kagent.kagent.fullnameOverride` instead of targeting a Service named after the release.
- The public muster `/` route and the kagent Namespace render only when their components are enabled.
- Validation messages, `required` errors and values comments name the keys of this chart's layout (`components.kagent.*`, `kagent.kagent.*`) instead of the fleet paths nothing here reads; the legacy-toggle hints probe the renamed `klaus-gateway`/`agent-sandbox` keys and no longer point at a nonexistent UPGRADE.md.
- `NOTES.txt` no longer aborts the render when `gateway.listeners` is empty.
- A `--check` failure's `*.curate` diff sidecars no longer mask the real diff on the next run; write runs clean them up.
- The overlay can no longer reintroduce a component-level `enabled` toggle that nothing reads.
- `renovate.json5` extends `renovate-custom.json5`, so the helmv3/helm-values manager disables actually apply before the next devctl regeneration.

[Unreleased]: https://github.com/giantswarm/agent-platform-standalone/tree/main
