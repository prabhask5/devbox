# Incus response fixtures

Response bodies of the Incus REST API calls devbox makes, for an instance named `devbox`. The fake
Incus server (`tests/fakes/incus`) is tested to answer with the same shapes, and the client
(`internal/incus`) to read them.

These files follow the Incus 6.0 API definition (`doc/rest-api.yaml` in the Incus repository);
values are anonymized.
