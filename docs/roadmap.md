# Roadmap

devbox 1.0 is one owner, one VM, one server: the scope of the [specs](spec/README.md). What is below is what would come next, roughly in the order it is likely to. None of it is promised; an issue is the place to argue for or against any of it.

## Next

- **More than one VM on a server.** Instance names are configurable everywhere already; what is missing is a gateway that fronts several, with a host name per VM, and a `devbox up` that knows which one it is talking about.
- **A local assistant in the agents app.** The app has the tab and the hub the session kind (`chat`) for an assistant that runs on your own hardware and talks over an API instead of a terminal. Hosting the model is not devbox's job; talking to one that you host is.
- **Errors from Codex.** Codex has no hook for a turn that fails, so the app cannot show one. When it gets one, devbox listens to it.
- **Gateway deploys on GitHub.** The hub's deploys show in GitHub's Deployments; the gateway's show only in Dokploy, which does not report them. A workflow that follows Dokploy's API could.

## Later, maybe

- **A native iOS app.** The agents app is an installable web app, and the hub's API is plain enough for a native client; nothing needs one yet.
- **Other hypervisors.** Everything devbox asks of the VM goes through a small client for Incus's API (start, stateful stop, state, exec, files). Another hypervisor that can save a VM's memory to disk would need the same five things.
- **More than one owner.** A team would need accounts, roles and per-user sessions, which is a different product; this stays a personal machine unless that changes.

## Not planned

Kubernetes, Windows or macOS as the server (the SSH client does run on macOS), and hosting models.
