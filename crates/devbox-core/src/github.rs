//! Asking the GitHub API, with `curl` run through [`System`]: no TLS is linked into devbox, and
//! the token never appears on a command line.

use std::time::Duration;

use crate::system::{Command, System};

/// The token to talk to GitHub with: `GITHUB_TOKEN` or `GH_TOKEN`, else what `gh auth token`
/// prints. `None` when there is none, which is not an error: GitHub is then not asked.
pub fn token(system: &dyn System) -> Option<String> {
    for name in ["GITHUB_TOKEN", "GH_TOKEN"] {
        if let Some(value) = system.env(name).filter(|value| !value.trim().is_empty()) {
            return Some(value.trim().to_owned());
        }
    }
    let output = system.run(&Command::new("gh", ["auth", "token"])).ok()?;
    Some(output.text()).filter(|token| output.success() && !token.is_empty())
}

/// The API to ask: `DEVBOX_GITHUB_API`, else GitHub's own.
pub fn api(system: &dyn System) -> String {
    system
        .env("DEVBOX_GITHUB_API")
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| "https://api.github.com".to_owned())
}

/// One GET of the GitHub API at `path` (with its query). The token travels on curl's standard
/// input, where no other process can see it.
///
/// # Errors
///
/// One line saying why there is no answer: curl could not run, the request failed, or the
/// answer is not JSON.
pub fn get(
    system: &dyn System,
    api: &str,
    token: &str,
    path: &str,
) -> Result<serde_json::Value, String> {
    let config = format!(
        "header = \"Authorization: Bearer {token}\"\nheader = \"Accept: application/vnd.github+json\"\nheader = \"X-GitHub-Api-Version: 2022-11-28\"\nuser-agent = \"devbox\"\n"
    );
    let url = format!("{}{path}", api.trim_end_matches('/'));
    let curl = Command::new(
        "curl",
        ["--silent", "--show-error", "--fail", "--max-time", "30", "--config", "-", &url],
    )
    .stdin(config)
    .timeout(Duration::from_secs(40));
    let output = system.run(&curl).map_err(|error| format!("couldn't run curl: {error}"))?;
    if !output.success() {
        let line = output.error_text().split_whitespace().collect::<Vec<_>>().join(" ");
        return Err(line.chars().take(200).collect());
    }
    serde_json::from_slice(&output.stdout).map_err(|_| "GitHub's answer is not JSON".to_owned())
}
