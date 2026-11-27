# An adopted host

The host files of a server that already runs a hand-built development VM behind Dokploy's
Traefik, anonymized: the Traefik route for the editor (`devbox.yml`) and the firewall rules
(`rules4.txt`). `tests/rust/tests/setup.rs` puts them on a fake machine together with an
Incus instance that is running, starts with the host and takes daily snapshots, and checks
what `devbox up --adopt` plans and does, and that `devbox down` puts every byte back.
