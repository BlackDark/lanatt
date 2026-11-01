# Podman finds the hook through hooks_dir, Docker through a runc wrapper

Both engines call the same `lanatt hook create|stop` (ADR-0001). How the hook gets into the OCI config differs.

Podman reads hook files from `hooks_dir`. The built-in default is only `/usr/share/containers/oci/hooks.d`, which packages own. lanatt's hook files go in `/etc/containers/oci/hooks.d`, and a `containers.conf.d` drop-in adds that directory. The files match on the `io.lanatt.attachments` annotation, so other containers never run the hook.

Docker has no hook directory and no per-container hook option. `lanatt-runc`, a symlink to `lanatt`, is registered as a Docker runtime and selected per container. On `create`, it adds the two hooks to the bundle's `config.json` when the annotation is present, then execs `runc` with the same arguments. Unannotated bundles go through unchanged.

## Considered options

- Install Podman hooks into `/usr/share/containers/oci/hooks.d`. Rejected: that directory is package-owned, and admin files there survive package removal unnoticed.
- Make `lanatt-runc` Docker's default runtime. Rejected: a lanatt bug would then break every container on the host. Selecting it per container limits the blast radius.
- A Docker plugin or a containerd NRI plugin. Rejected for now: both are more machinery than a wrapper.
