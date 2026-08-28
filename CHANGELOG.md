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

### Fixed

- The kagent UI route's direct backendRef honors `kagent.kagent.fullnameOverride` instead of targeting a Service named after the release.
- The public muster `/` route and the kagent Namespace render only when their components are enabled.
- Validation messages, `required` errors and values comments name the keys of this chart's layout (`components.kagent.*`, `kagent.kagent.*`) instead of the fleet paths nothing here reads; the legacy-toggle hints probe the renamed `klaus-gateway`/`agent-sandbox` keys and no longer point at a nonexistent UPGRADE.md.
- `NOTES.txt` no longer aborts the render when `gateway.listeners` is empty.
- A `--check` failure's `*.curate` diff sidecars no longer mask the real diff on the next run; write runs clean them up.
- The overlay can no longer reintroduce a component-level `enabled` toggle that nothing reads.
- `renovate.json5` extends `renovate-custom.json5`, so the helmv3/helm-values manager disables actually apply before the next devctl regeneration.

[Unreleased]: https://github.com/giantswarm/agent-platform-standalone/tree/main
