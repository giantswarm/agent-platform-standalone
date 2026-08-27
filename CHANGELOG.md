# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `hack/curate.sh`, the generator that produces `Chart.yaml`, `Chart.lock` and `values.yaml` from the `agent-platform` fleet chart through a deny-unknown transform configured in `curate.yaml` and `overlay/vanilla.yaml`.
- Umbrella chart with one dependency per component (`muster`, `valkey`, `kagent`, `agentgateway`, `agent-platform-mcps`, `klaus-gateway`, `dicebear`, `agent-sandbox`, `backstage`, `cloudnative-pg`), toggled by `components.<name>.enabled`, pinned to exact versions.
- Wiring templates (routes, agentgateway data-plane Gateway, network policies, kagent CRs, CloudNativePG Cluster) copied from `agent-platform-connectivity` and rewired to the umbrella values layout.
- `values.schema.json`, strict for the umbrella-owned keys, opaque for component blocks.
- `examples/kind-lab-dex.yaml` and the `verify` CI job (`make verify`).
- The `global.*` input contract: `global.domain` (public hostnames derive from it, each overridable per component), `global.identity` (`issuerUrl`, `clientId`, `existingSecret`, `ca`), `global.gatewayApi.parentRefs`, `global.networkPolicy` (moved from `networkPolicy`), `global.observability.metrics.serviceMonitor` and `global.observability.traces.otlp`.
- `gatewayApi.gateway.create`: the chart-owned agentgateway Gateway becomes the public edge with an HTTPS listener for `*.<global.domain>` (`tls.secretName`, `serviceType`); the layer-1 routes to the agentgateway Service are not rendered in that mode.
- Vanilla defaults (`overlay/vanilla.yaml`): network policies off (`kubernetes` flavor when on), `kyvernoPolicies` off, ServiceMonitors and PodMonitors off, no OTLP endpoint, dicebear off, agent-sandbox pod-security policy off.
- `ingress.httpRoute.timeouts` (`request: 0s`) on the umbrella-owned routes; the Envoy `BackendTrafficPolicy` stays opt-in.
- `components.backstage`: the umbrella renders the Backstage app-config ConfigMap (login provider from `global.identity`, one installation named after the release, the in-cluster Kubernetes and muster entries, `app.extensions` limited to the Agent Platform pages, `app.rootRedirect: /agent-platform`) and the `backstage.<domain>` route; `hostname`, `parentRefs`, `extraScopes`, `disabledExtensions`, `skillsRepositories` inputs.
- `overlay/contract.yaml` (the contract, never reverted) next to `overlay/vanilla.yaml`; `examples/giantswarm.yaml` is generated from the vanilla overlay and `overlay/giantswarm.yaml`.
- `make verify-decisions`: render assertions for the vanilla defaults, the derived hostnames, the Giant Swarm example, the network-policy flavors, the edge Gateway and the guards.
- The CloudNativePG `Cluster` carries `helm.sh/resource-policy: keep`; its PodMonitor follows `global.observability.metrics.serviceMonitor`.

### Changed

- `networkPolicy` is `global.networkPolicy`.
- The kagent controller ServiceMonitor reads `global.observability.metrics.serviceMonitor` (`enabled`, `labels`, `interval`).
- The kagent JWT policy issuer defaults to `global.identity.issuerUrl`; the kagent oauth2-proxy `oidc-issuer-url`, `client-id` and `redirect-url` arguments derive from `global.identity` and `global.domain`.
- Route label and annotation maps under `ingress.httpRoute` and `ingress.backendTrafficPolicy` accept any key (the schema closed them).

[Unreleased]: https://github.com/giantswarm/agent-platform-standalone/tree/main
