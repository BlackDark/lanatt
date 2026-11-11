# containerd NRI for lanatt hooks

Status: research, no decision. Issue: [BlackDark/lanatt#3](https://github.com/BlackDark/lanatt/issues/3).

Sources, cloned shallow from GitHub at these HEADs:
- `containerd/nri` tag `v0.12.5` (commit `5df40b41`, published 2026-10-23)
- `containerd/containerd` tag `v2.4.2` (commit `b7a25261`, 2026-10-13)
- `moby/moby` @ `36d6bbbaa1`
- `containers/podman` @ `326546529d` (main, 2026-11-11)
- `containers/common` @ `a5ccdae8`
- `docker/docs` (release notes only)

## 1. Docker release enabling NRI; default

- **Answer:** Added in `docker-v29.2.0`. Release notes call it "experimental NRI support". Not in `docker-v29.1.0`.
- **Default:** OFF. `NRIOpts.Enable` zero value is `false`; `NewNRI` logs "NRI is disabled" and returns.
- **Config:** `dockerd --nri-opts enable=true[,plugin-path=..,plugin-config-path=..,socket-path=..]` or `daemon.json` `"nri-opts": {"enable": true, ...}`.
- **Refs:**
  - `docs/content/manuals/engine/release-notes/29.md:877-889` (heading `## 29.2.0`, line 889 "Add experimental NRI support", PRs moby#51634–51712)
  - `moby/daemon/pkg/opts/nri_opts.go`
  - `moby/daemon/config/config.go:94,306`
  - `moby/daemon/internal/nri/nri.go:67-72`
  - Import commit `4941b36883` (2025-12-08); tag `docker-v29.2.0` = `bca62c02` contains it; `docker-v29.1.0` does not.
- **docs.docker.com:** no NRI page found. `https://docs.docker.com/engine/daemon/nri/` returns 404. Docs page: UNKNOWN.

## 2. NRI under rootless Docker

- **Answer:** UNKNOWN (code path exists; not tested).
- NRI runs inside the `dockerd` process (`daemon/create.go:253`), not inside containerd. Containerd is not the NRI host here.
- Rootless branch exists: `moby/daemon/internal/nri/nri.go:282-300` (`setDefaultPaths` uses RootlessKit libexec/config home).
- Integration tests skip rootless: `moby/integration/daemon/nri/nri_test.go:24,81,143` (`skip.If(t, testEnv.IsRootless)`).
- Embedded containerd in dockerd excludes NRI: `moby/daemon/internal/containerd/server/embedded/server.go:27-28`.

## 3. Can an NRI plugin add createRuntime / poststop hooks?

- **Protocol: YES.** `ContainerAdjustment.hooks` (field 5) exists in `nri/pkg/api/api.proto:689-698`. `Hooks` message (`api.proto:501-508`) has `create_runtime`, `poststop` and others.
- **containerd CRI path: applied.** `nri/pkg/runtime-tools/generate/generate.go:152-163` (`Adjust` → `AdjustHooks`); `:273-293` maps CreateRuntime and Poststop to the OCI spec. Used by `containerd/internal/cri/nri/nri_api_linux.go:305-372`.
- **containerd default validator: does not reject hooks by default.** `containerd/internal/nri/config.go:58` (`DefaultValidatorConfig{}`); `nri/plugins/default-validator/default-validator.go:38,134` (`RejectOCIHookAdjustment` defaults false).
- **Moby/Docker: REJECTS hooks.** `moby/daemon/internal/nri/nri.go:419-427` returns `unsupported container adjustments: hooks`; annotations also rejected (`nri.go` `checkForUnsupportedAdjustments`). Test pins this: `moby/integration/daemon/nri/nri_test.go:103-105`.
- **Which containers NRI reaches:** only registered domains. In-tree only CRI registers (`containerd/internal/cri/nri/nri_api_linux.go:73`, `RegisterDomain`, namespace `k8s.io`). Moby registers none. Domain code is under `containerd/internal/nri` (Go `internal`, not importable out of tree). Whether an external NRI plugin gets `CreateContainer` for non-CRI namespaces (e.g. `moby`): UNKNOWN.
- containerd NRI on by default since 2.0: `containerd/docs/NRI.md` ("Starting with containerd 2.0 ... enabled by default"), plugin `io.containerd.nri.v1.nri`.

## 4. Podman NRI support

- **Answer: NO NRI in Podman source.** Grep of `containers/podman` and `containers/common` (excluding vendor) finds no NRI code or docs. Only vendored moby API types mention NRI.
- Podman hook path (for reference): `common/pkg/hooks/hooks.go:125-130` maps `createRuntime`/`prestart` and `poststop`; `podman/libpod/container_internal.go:2385-2420` runs poststop.
- Podman NRI config: N/A. Podman docs NRI mention: none found. Podman release-tag check (v6.2.0): UNKNOWN, only main checked.

## Implications for lanatt

- Docker (moby 29.2+): NRI cannot add lanatt hooks; moby rejects them. `lanatt-runc` wrapper stays for Docker.
- containerd: NRI can carry `createRuntime` and `poststop` (v0.12.5). Default in-tree path only reaches CRI `k8s.io` containers. Using it for non-CRI containers needs containerd-side support: UNKNOWN.
- Podman: no NRI. Keep OCI `hooks.d` path and the `examples/podman/containers.conf.d` drop-in.
- Next test (not done): containerd 2.4.2 + external NRI plugin on the Debian test VM; check if a `moby`/`default` namespace container gets a `CreateContainer` event and whether hooks land in the spec.
