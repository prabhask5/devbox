# Incus response fixtures

Response bodies of the Incus REST API calls devbox makes, for an instance named `devbox`. The fake
Incus server (`tests/fakes/incus`) is tested to answer with the same shapes, and the clients
(`internal/incus`, `devbox_core::incus`) to read them.

`instance.json`, `state-running.json` and `error-not-found.json` are what a real Incus 6.0
answered in the nightly real-VM run (`scripts/nightly.sh` records them into its `nightly`
artifact), with addresses, ids and the cloud-init data replaced. The others — a stopped
instance's state, a state change and its operations — follow the Incus 6.0 API definition
(`doc/rest-api.yaml` in the Incus repository), which that run exercises through the gateway.
