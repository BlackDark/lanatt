# CNI plugin support across engines

Status: research, no decision. Input for [#25](https://github.com/BlackDark/lanatt/issues/25) and [cni-delegation.md](cni-delegation.md). Checked against primary sources on 2026-11-11.

## 1. Podman

**Answer:** netavark is the default backend since Podman 4.0. CNI is a build-tagged legacy option since 5.0 and removed in 6.0. Podman does not need `containernetworking-plugins` on Debian 13.

| Podman | Default backend | CNI status | Source |
|---|---|---|---|
| 4.0 | netavark for new installs | CNI still present; used for existing installs | [RELEASE_NOTES 4.0.0](https://github.com/containers/podman/blob/v4.0.0/RELEASE_NOTES.md) |
| 4.4 | netavark "advised and default" | "deprecated" | [RELEASE_NOTES 4.4.0](https://github.com/containers/podman/blob/main/RELEASE_NOTES.md) (section 4.4.0) |
| 4.8 | netavark | CNI listed as an upcoming deprecation; not yet gated | [RELEASE_NOTES 4.8.0](https://github.com/containers/podman/blob/main/RELEASE_NOTES.md) |
| 5.0 | netavark | "not enabled by default" | [RELEASE_NOTES 5.0.0](https://github.com/containers/podman/blob/main/RELEASE_NOTES.md) |
| 5.8.10 | netavark | `cni` build tag only; Makefile adds it only for FreeBSD | [podman v5.8.10 Makefile:83-84](https://github.com/containers/podman/blob/v5.8.10/Makefile) |
| 6.0.0 | netavark only | "Support for CNI networking has been removed" | [RELEASE_NOTES 6.0.0](https://github.com/containers/podman/blob/main/RELEASE_NOTES.md) (Breaking Changes) |

Backend selection code (`containers/common` v0.62.2, used by Podman 5.8.10 per its `go.mod`):
- Without `cni` tag: `libnetwork/network/interface_cni_unsupported.go` (`//go:build (linux || freebsd) && !cni`) returns `netavark` unconditionally. [src](https://github.com/containers/common/blob/v0.62.2/libnetwork/network/interface_cni_unsupported.go)
- With `cni` tag: `libnetwork/network/interface.go` `NetworkBackend()`: explicit `network_backend` wins; else sentinel file `defaultNetworkBackend`; else CNI only if no netavark binary, or existing containers/images/CNI networks. [src](https://github.com/containers/common/blob/v0.62.2/libnetwork/network/interface.go)
- `containers.conf` docs (v0.60.0) still describe the old CNI-or-netavark default. [docs](https://github.com/containers/common/blob/v0.60.0/docs/containers.conf.5.md) (lines 378-382)

Packaging:
- Debian 13 `podman` 5.4.2: `Depends: netavark`; `Suggests: containernetworking-plugins`. [control](https://sources.debian.org/src/podman/5.4.2+ds1-2/debian/control/) (lines 79, 93); [packages.debian.org/trixie/podman](https://packages.debian.org/trixie/podman)
- Debian 13 `podman` build tags: `BUILDTAGS = apparmor seccomp selinux systemd libsubid`. No `cni`, so CNI code is not compiled in. [rules](https://sources.debian.org/src/podman/5.4.2+ds1-2/debian/rules/) (line 48)
- Podman Go modules still import `containernetworking/plugins/pkg/ns` (Go library, not binaries): `libpod/networking_linux.go`. Podman 5.8.10 pins `plugins v1.8.0` directly; v6.2.0 has `plugins v1.9.1 // indirect`. [v5.8.10 go.mod:14](https://github.com/containers/podman/blob/v5.8.10/go.mod); [v6.2.0 go.mod:105](https://github.com/containers/podman/blob/v6.2.0/go.mod)
- Podman 4 Debian version and its package relations: UNKNOWN (not checked).

## 2. Docker (moby)

**Answer:** No. Docker's libnetwork does not use CNI. Its `bridge`, `macvlan`, `ipvlan` drivers call netlink directly.

- Searched moby `v28.5.2` for `cni|containernetworking` in `libnetwork/` and `daemon/` (non-test, non-vendor): no hits. [moby v28.5.2](https://github.com/moby/moby/tree/v28.5.2)
- Drivers: `libnetwork/drivers/{bridge,host,ipvlan,macvlan,null,overlay,remote,windows}`. macvlan creates links via `netlink` (`libnetwork/drivers/macvlan/macvlan_joinleave.go:41`).
- Debian 13 `docker.io` 26.1.5: no `containernetworking` relation. [packages.debian.org/trixie/docker.io](https://packages.debian.org/trixie/docker.io)
- Moby `vendor.mod` lists `containernetworking/plugins v1.7.1` as indirect. Pulling module is not traced; no dockerd code path searched uses it.
- Docker 29.x source: not re-checked (latest tag found: `docker-v29.9.3`). Expected same. UNKNOWN until checked.

## 3. containerd / nerdctl

**Answer:** containerd core does not need CNI. The CRI plugin does (Kubernetes pod networking). nerdctl needs CNI plugins for `nerdctl run`.

containerd `v2.1.0`:
- Core: `pkg/netns` imports `containernetworking/plugins/pkg/ns` (library). `cmd/ctr` imports `containerd/go-cni` (`ctr run --cni`, `ctr task kill`). [src](https://github.com/containerd/containerd/tree/v2.1.0)
- CRI: `internal/cri/server/status.go:44` reports `NetworkReady`; `internal/cri/config/config_unix.go:26` default bin dir `/opt/cni/bin`. [config_unix.go](https://github.com/containerd/containerd/blob/v2.1.0/internal/cri/config/config_unix.go)
- Docs: "Typically, you will have to install runc and CNI plugins" [getting-started.md](https://github.com/containerd/containerd/blob/v2.1.0/docs/getting-started.md) (line 9, step 3 lines 64-67).
- `go.mod`: `containerd/go-cni v1.1.12`, `containernetworking/plugins v1.7.1`, `containernetworking/cni v1.3.0`. [go.mod](https://github.com/containerd/containerd/blob/v2.1.0/go.mod)
- Debian 13 `containerd` 1.7.24: `Suggests: containernetworking-plugins`. [packages.debian.org/trixie/containerd](https://packages.debian.org/trixie/containerd)

nerdctl `v2.4.2` (latest release):
- README: "CNI plugins: for using `nerdctl run`. v1.1.0 or later is highly recommended." `nerdctl-full` tarball bundles them; plain tarball does not. [README.md](https://github.com/containerd/nerdctl/blob/v2.4.2/README.md) (lines 133-140)
- `docs/cni.md`: basic networks need "CNI plugin be installed". [cni.md](https://github.com/containerd/nerdctl/blob/v2.4.2/docs/cni.md)
- Bundled plugin version: `Dockerfile` `CNI_PLUGINS_VERSION=v1.9.1@BINARY` (line 22). `go.mod`: `containernetworking/plugins v1.9.1`, `go-cni v1.1.14`. [Dockerfile](https://github.com/containerd/nerdctl/blob/v2.4.2/Dockerfile); [go.mod](https://github.com/containerd/nerdctl/blob/v2.4.2/go.mod)
- Bridge isolation needs plugins >= 1.1.0 (`docs/command-reference.md` line 1288).

## 4. Debian 13 `containernetworking-plugins`

- Version in trixie: `1.1.1+ds1-3`. [packages.debian.org/trixie/containernetworking-plugins](https://packages.debian.org/trixie/containernetworking-plugins); source package `golang-github-containernetworking-plugins`.
- Binaries: bridge, ipvlan, loopback, macvlan, ptp, vlan, host-device; IPAM dhcp, host-local, static; tuning, portmap, bandwidth, sbr, firewall. (Same page.)
- Reverse dependencies: packages.debian.org has no reverse-depends search via URL; found by manual check only:
  - `podman`: Suggests.
  - `containerd`: Suggests.
  - Full list: UNKNOWN.
- Upstream latest: `v1.10.0` (2026-11-09), `v1.9.1` (2026-03-16), `v1.9.0`, `v1.8.0`. [releases](https://github.com/containernetworking/plugins/releases)

## 5. CNI spec and plugin versions

- Spec: `1.1.0` is current. Adds `STATUS` and `GC` commands vs 1.0.0. [SPEC.md v1.1.0](https://github.com/containernetworking/cni/blob/spec-v1.1.0/SPEC.md) (lines 330, 366); [SPEC.md v1.0.0](https://github.com/containernetworking/cni/blob/spec-v1.0.0/SPEC.md).
- `containernetworking/cni` library `v1.3.1`: `Current()` = `1.1.0`; `All` supports `0.1.0`..`1.1.0`. [pkg/version](https://github.com/containernetworking/cni/blob/v1.3.1/pkg/version/version.go) (lines 27, 39)
- Debian `1.1.1` plugins: `go.mod` `cni v1.0.1`; `version.All` ends at `1.0.0`. [plugins v1.1.1 go.mod](https://github.com/containernetworking/plugins/blob/v1.1.1/go.mod); [pkg/version v1.1.1](https://github.com/containernetworking/cni/blob/v1.1.1/pkg/version/version.go) (line 39)
- Upstream `v1.10.0`: `go.mod` `cni v1.3.1` → supports `1.1.0`. Not verified by test.
- Version gate: `pkg/skel/skel.go` `checkVersionAndCall` returns `ErrIncompatibleCNIVersion` when config `cniVersion` is not in the plugin's supported list. [skel.go v1.1.0](https://github.com/containernetworking/cni/blob/v1.1.0/pkg/skel/skel.go) (lines 156-163)
- Risk: a `cniVersion: 1.1.0` config on Debian 1.1.1 fails ADD with incompatible-version error. Configs in this repo's research use `1.0.0`, which works with both.
- Runtime check: `VERSION` command reports supported versions (`skel.go` line 136).

## Implications for lanatt

- **Podman:** safe without CNI plugins. Netavark is the default for new installs since 4.0 and the only backend in 6.x. 4.x and 5.x can still run CNI when a CNI config exists. Debian 13 Podman does not depend on `containernetworking-plugins` (Suggests only). Do not require CNI for Podman users.
- **Docker:** no CNI involvement. A hard dependency adds nothing for Docker users; lanatt's Docker path does not need it.
- **containerd / nerdctl:** containerd core does not need CNI; nerdctl does for `run`. Treat as available-by-engine, not guaranteed. Pick plugins with an explicit check, don't assume.
- **Required vs optional:** keep built-in macvlan as the required path. Make CNI plugins an opt-in backend with a detect-and-fail check at startup, not a package `Depends` (Debian lists them as Suggests for podman/containerd).
- **Version pin:** emit `cniVersion` matching the detected plugin (`1.0.0` safe for 1.1.1 and 1.9.x/1.10.x; `1.1.0` needs plugins that implement 1.1.0). Validate via the `VERSION` command before ADD.
- **Unknowns to close before a decision:** Docker 29 source recheck, full Debian reverse-dependency list, Podman 4.x Debian packaging, and actual plugin behavior for `cniVersion 1.1.0` on Debian 1.1.1 (inferred from source, not run).
