### Checklist

- [ ] Conventional PR title; the squash commit takes it. Every type except
      `docs` and `style` cuts a release (`feat` the minor, a breaking change the
      major, the rest the patch); a docs-only PR releases nothing and ships with
      the next release.
- [ ] Generator inputs changed (`curate.yaml`, `overlay/*.yaml`, a template under
      `templates.extra`): `make curate`, then `pre-commit run --all-files`, and
      the regenerated files are committed. `Chart.yaml`, `Chart.lock`,
      `values.yaml` and the generated templates are never edited by hand.
- [ ] `make verify` passes (what CI runs). A change that adds a rendered shape
      or a render-time guard adds an assertion to the matching `verify-*` target
      in `Makefile.custom.mk`.
- [ ] Behaviour on a cluster changed: proven in agentlab with `platform.apsRef`
      at this branch's commit.
- [ ] `CHANGELOG.md` has the entry under `[Unreleased]`.
