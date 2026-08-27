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

[Unreleased]: https://github.com/giantswarm/agent-platform-standalone/tree/main
