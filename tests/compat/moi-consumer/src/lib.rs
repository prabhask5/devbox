//! devbox's library crates as moi uses them: through moi's own `host::Host`.
//!
//! [`host`] is a copy of moi's trait. [`OnMoi`] is the adapter moi has: devbox's `System` on
//! top of a moi host, each method mapped onto the method of the same name. [`FixtureHost`] is a
//! moi host that exists only in memory, for the scenarios in `tests/`.

pub mod host;

mod adapter;
mod fixture;

pub use adapter::OnMoi;
pub use fixture::FixtureHost;
