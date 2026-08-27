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
components:            # umbrella-owned: one entry per dependency
  kagent:
    enabled: true      # the Helm dependency condition
    controllerRoute:   # wiring the umbrella renders for kagent
      enabled: true
ingress:               # umbrella-owned wiring blocks
gateway:
networkPolicy:
kagent:                # the kagent chart's own values, forwarded verbatim
  kagent:              # nested: the Giant Swarm kagent chart vendors upstream as a subchart
    controller: {}
```

- `components.<name>.enabled` turns a dependency on or off. Hyphenated names
  need quotes on the command line: `--set 'components.klaus-gateway.enabled=true'`.
- `global`, `components` and the wiring blocks (`ingress`, `gateway`,
  `networkPolicy`, `kyvernoPolicies`, `extraObjects`, `postgres`) are validated
  strictly by `values.schema.json`. A component block is opaque here; the
  component chart validates it with its own schema.
- `kagent` and `agentgateway` values are nested one level (`kagent.kagent.*`,
  `agentgateway.agentgateway.*`) because the Giant Swarm charts vendor the
  upstream chart as a subchart. A later release flattens them.

## Install

```sh
helm dependency build helm/agent-platform-standalone
helm install agent-platform helm/agent-platform-standalone \
  --namespace agent-platform --create-namespace \
  -f examples/kind-lab-dex.yaml
```

`examples/kind-lab-dex.yaml` is a kind quick start with agentgateway as the
edge and a lab Dex as the identity provider. It is lab-only. The prerequisites
manifest for the lab Dex is not part of this release yet.

## The chart is generated

`Chart.yaml`, `Chart.lock` and `values.yaml` are generated. Do not edit them.
The generator `hack/curate.sh` reads the fleet meta-package
`giantswarm/agent-platform` and the `agent-platform-connectivity` chart at the
version pinned in `curate.yaml`, and:

1. builds the dependency list from the fleet chart's component list, plus the
   extra dependencies declared in `curate.yaml` (`backstage`, `cloudnative-pg`);
2. resolves each major-bounded range (`5.x`) to the newest published version and
   writes that exact pin into `Chart.yaml`;
3. transforms the fleet values into the layout above: fleet-only keys dropped,
   each component's `enabled` moved to `components.<name>.enabled`, umbrella
   wiring lifted out of the component blocks, blocks renamed to the chart name
   and nested under the wrapper's subchart key;
4. merges `overlay/vanilla.yaml` last;
5. runs `helm dependency update` to refresh `Chart.lock`, and keeps the committed
   file when the pins did not change.

The transform is deny-unknown. Every top-level key of the fleet and
connectivity values must have a rule in `curate.yaml`; a fleet rename or a new
Giant Swarm-only default fails the run instead of leaking into the defaults.
Running the generator twice yields no diff.

```sh
make curate          # regenerate
make verify          # what CI runs: go test, curate --check, render every example, schema checks
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
  `Chart.lock` fails the build.
- Renovate does not touch chart dependencies. The generator owns the BOM.

The templates were copied once from `agent-platform-connectivity` and evolve
here. Component values are regenerated from the fleet chart, so the defaults
cannot drift.
