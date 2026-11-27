//! Configuration, the Incus client and the plan/apply engine behind `devbox up`, `down`, `status`
//! and `doctor`, built to be embedded by other Rust programs.

// The types generated from the contracts name this crate's types by their public path.
extern crate self as devbox_core;

pub mod config;
pub mod fake;
pub mod github;
pub mod incus;
pub mod password;
pub mod proto;
pub mod schema;
pub mod secret;
pub mod system;
pub mod time;
