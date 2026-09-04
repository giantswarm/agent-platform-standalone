# agent-platform-standalone

The Giant Swarm Agent Platform as one plain Helm chart, generated from the
fleet charts. Most of the chart is generator output. Change the inputs, run
the generator, verify. The README's section "The chart is generated" explains
the generator; this file says which files are yours to edit.

## Generated, never edited by hand

Output of `hack/curate.sh` (`make curate`). A hand edit is overwritten by the
next run and fails `make verify-curate` in CI:

- `helm/agent-platform-standalone/Chart.yaml`, `Chart.lock`, `values.yaml`
- `helm/agent-platform-standalone/templates/**`, except the files `curate.yaml`
  lists under `templates.extra`
- `examples/giantswarm.yaml`

Output of the pre-commit hooks (`pre-commit run --all-files`, commit the
result):

- `helm/agent-platform-standalone/values.schema.json` (the helm schema plugin,
  driven by the `# @schema` comments in `values.yaml`)
- `helm/agent-platform-standalone/README.md` (helm-docs, from
  `README.md.gotmpl`)

Output of devctl (the `zz_generated.*` files, the root `Makefile`,
`Makefile.gen.app.mk`, `renovate.json5`, `.circleci/config.yml`,
`.nancy-ignore.generated`). Repo-specific configuration goes next to them:
`Makefile.custom.mk`, `renovate-custom.json5`, `.circleci/custom.yml`.

## Hand-written

- `curate.yaml`: the generator's input. The fleet pin, one dependency per
  component (exact `version`, major-bounded `range`), the deny-unknown `keys`
  rules for every top-level fleet key (`keep`, `drop`, `dependencies`,
  `wiring`, `component`, `lift`), the `# @schema` `annotations` injected into
  the generated values, and the `templates` section (`rewrite`, exact-once
  `patch` entries, the `extra` list of templates this chart owns).
- `overlay/contract.yaml`: the umbrella's input contract and the whole
  `components.<name>` block of the umbrella components (`modelServing`,
  `kserve`). `overlay/vanilla.yaml`: fleet defaults a vanilla cluster turns
  off. `overlay/giantswarm.yaml`: inputs of the generated Giant Swarm example.
- The templates under `templates.extra`: `NOTES.txt`, `templates/backstage/`,
  `templates/model-serving/`, `templates/kserve/`,
  `templates/model-manager/model-serving-validate.yaml`,
  `templates/kagent/ui-redirect-validate.yaml`,
  `templates/mcp-kubernetes/mcpserver.yaml`.
- `helm/agent-platform-standalone/files/`: the shipped serving presets, chat
  templates and the preset JSON schema. `helm/agent-platform-standalone/ci/`:
  the CI render values.
- `hack/curate/` (the generator, Go, with its tests) and `hack/presets/` (the
  preset validator).
- `examples/kind-lab-dex.yaml`, `prerequisites/`, `tests/ats/`,
  `.ats/main.yaml`, `.github/workflows/curate-regen.yaml`.
- `README.md` (root), `helm/agent-platform-standalone/README.md.gotmpl`,
  `CHANGELOG.md`.

## The loop

```sh
make curate                  # regenerate Chart.yaml, values.yaml, Chart.lock, templates, examples/giantswarm.yaml
pre-commit run --all-files   # regenerate values.schema.json and the chart README
make verify                  # what CI runs
```

- A component default belongs to the fleet chart (`giantswarm/agent-platform`)
  or to `overlay/*.yaml`, never to `values.yaml`.
- A new top-level fleet key needs a `keys` rule; the transform is deny-unknown
  and fails the run otherwise. A new open map or array under an umbrella-owned
  key needs an `annotations` entry, or the generated schema freezes its shape.
- A wrong line in a copied connectivity template is fixed upstream first
  (`giantswarm/agent-platform`), then the pin is bumped. A `templates.patch`
  entry is the exception for what upstream cannot carry; it is an exact-once
  find/replace on the post-rewrite text and names the upstream fix that
  retires it. A template this chart alone owns is listed under
  `templates.extra`.
- A change that adds a rendered shape or a render-time guard adds a check to
  the matching `verify-*` target in `Makefile.custom.mk`; `make verify` is the
  CI gate.
- Behaviour on a cluster is proven in agentlab (`giantswarm/agentlab`) with
  `platform.apsRef` set to the branch's commit before the PR merges.

## Dependency pins

The pins in `curate.yaml` are the bill of materials. Renovate bumps an exact
`version` (the `# registry:` hint above each pin is the org-wide convention),
and the `curate-regen` workflow regenerates the chart on the Renovate branch.
Never run `helm dependency update`, never edit `Chart.yaml` or `Chart.lock`,
never widen a `range` as part of a bump. CI runs `helm dependency build` and
`hack/curate.sh --check`, which validates the committed pins without asking the
registry for newer versions.

## Commits and releases

Conventional commit titles; the PR squashes to one commit, and a single-commit
PR squashes with that commit's message. `feat` and `fix` cut a release through
the auto-release workflow and publish the chart to
`oci://gsoci.azurecr.io/charts/giantswarm/agent-platform-standalone`; `docs`
and `chore` do not. Comment-only fixes that must reach the published chart
README are `fix` commits.
