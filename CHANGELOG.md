# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow semver once 1.0.0 ships.

## Unreleased

- Specification, milestones and agent workflow (spec-first start of the project).
- Gateway core: wake with phases and learned ETAs, restore-to-cold fallback, idle sleep, HTTP/WebSocket/SSH proxying, wake authentication, health and status endpoints, unconfigured mode and config hot reload.
- Contracts: JSON Schemas for every message and config file with generated Go, Rust and TypeScript types; config parsing that reports every problem at once; argon2id password hashes shared by Go and Rust.
- Repository scaffold: the `devbox-gateway`, `devbox-hub` and `devbox` binaries with every command present but not implemented, the web workspace, CI, and the hygiene and budget checks.
