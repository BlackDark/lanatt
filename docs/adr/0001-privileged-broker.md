# A root broker moves the attachment in

Rootless Podman and rootless Docker cannot create an attachment, because the host NIC is outside their network namespace. lanatt is a root daemon: an OCI `createRuntime` hook, running as the workload user, asks it to create the attachment and move it into the workload netns before the workload process starts. A refusal fails the start.

## Considered options

- Leave those workloads rootful so the engine creates the attachment. Rejected: only this daemon should stay privileged.
- Watch labels and attach after the process has started. Rejected: the process can bring its network up before the interface exists.
- A Podman `ExecStartPost` hook. Rejected: Docker cannot share that path.
