# Podman finds the hook through hooks_dir, Docker through a runc wrapper

Both engines call the same `lanatt hook create|stop` (ADR-0001). How the hook gets into the OCI config differs.

Podman reads hook files from `hooks_dir`. The built-in default is only `/usr/share/containers/oci/hooks.d`, which packages own. lanatt's hook files go in `/etc/containers/oci/hooks.d`, and a `containers.conf.d` drop-in adds that directory. The files match on the `io.lanatt.attachments` annotation, so other containers never run the hook.

Docker has no hook directory and no per-container hook option. `lanatt-runc`, a symlink to `lanatt`, is registered as a Docker runtime and selected per container. On `create`, it adds the two hooks to the bundle's `config.json` when the annotation is present, then execs `runc` with the same arguments. Unannotated bundles go through unchanged.

## Considered options

- Install Podman hooks into `/usr/share/containers/oci/hooks.d`. Rejected: that directory is package-owned, and admin files there survive package removal unnoticed.
- Make `lanatt-runc` Docker's default runtime. Rejected: a lanatt bug would then break every container on the host. Selecting it per container limits the blast radius.
- A Docker network driver plugin (`docker network create -d lanatt`). Rejected:
  - Rootful Docker gains nothing from it, because its built-in `macvlan` driver already does this.
  - Under rootless Docker, the plugin only creates the interface, and `dockerd` must move it into the container. Rootless `dockerd` runs inside RootlessKit's user and network namespaces, so it cannot reach a macvlan on the host NIC. This is the same wall as ADR-0001.
  - Managed plugins also expect a root `dockerd`.
- Authorization or volume plugins. Rejected: neither has a step in network setup.
- A containerd NRI plugin that adds the hooks to every annotated container, without wrapping `runc`. Deferred: it would be the cleaner form of the wrapper once Docker exposes NRI dependably. Only the wiring would change, not the daemon or the hook. Tracked in #3.

## Consequences

- `lanatt-runc` execs `runc`. Docker set up with another OCI runtime, such as `crun`, needs a configurable target. Tracked in #2.
- Docker needs `--annotation`, so Docker 24 or later.
- The Docker command line is less obvious than `--network`: a workload sets both `--runtime lanatt` and the annotation.
