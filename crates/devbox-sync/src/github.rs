//! Finding the owner's repositories on GitHub that are not on this machine yet, and cloning them.

use std::path::Path;
use std::time::Duration;

use devbox_core::github::get;
use devbox_core::proto::DotfilesManifestSync;
use devbox_core::system::{Command, System};

use crate::git;
use crate::text::{glob, one_line};

/// A repository on GitHub.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Remote {
    pub name: String,
    pub ssh_url: String,
    pub https_url: String,
}

const PER_PAGE: usize = 100;
const MAX_PAGES: usize = 50;

/// The repositories the manifest's rules select, in the order GitHub lists them.
pub(crate) fn list(
    system: &dyn System,
    api: &str,
    token: &str,
    rules: &DotfilesManifestSync,
) -> Result<Vec<Remote>, String> {
    // Without rules about owners, "what I own" is exactly the list. With them, organization
    // repositories are listed too and the owners decide.
    let everything_owned = rules.owner.is_none() && rules.include_orgs.is_empty();
    let affiliation = if everything_owned { "owner" } else { "owner,organization_member" };
    let mut owners: Vec<String> = rules.include_orgs.iter().map(|org| org.to_lowercase()).collect();
    if !everything_owned {
        let primary = match &rules.owner {
            Some(owner) => owner.clone(),
            None => {
                get(system, api, token, "/user")?["login"].as_str().unwrap_or_default().to_owned()
            }
        };
        owners.push(primary.to_lowercase());
    }

    let mut found: Vec<Remote> = Vec::new();
    for page in 1..=MAX_PAGES {
        let path = format!("/user/repos?affiliation={affiliation}&per_page={PER_PAGE}&page={page}");
        let listing = get(system, api, token, &path)?;
        let repos = listing.as_array().ok_or("GitHub's answer is not a list of repositories")?;
        for repo in repos {
            let text = |key: &str| repo[key].as_str().unwrap_or_default().to_owned();
            let name = text("name");
            let owner = repo["owner"]["login"].as_str().unwrap_or_default().to_lowercase();
            let wanted = (everything_owned || owners.contains(&owner))
                && (rules.archived || repo["archived"].as_bool() != Some(true))
                && !rules.exclude.iter().any(|pattern| glob(pattern, &name));
            // A name is a directory in the code directory: nothing else may come of it.
            let safe = !name.is_empty() && !name.starts_with('.') && !name.contains(['/', '\\']);
            if wanted && safe && !found.iter().any(|known| known.name.eq_ignore_ascii_case(&name)) {
                found.push(Remote { name, ssh_url: text("ssh_url"), https_url: text("clone_url") });
            }
        }
        if repos.len() < PER_PAGE {
            break;
        }
    }
    Ok(found)
}

/// Makes sure SSH knows GitHub's host key, so the first clone does not stop to ask.
fn know_github(system: &dyn System, home: &Path) {
    let known_hosts = home.join(".ssh/known_hosts");
    let known = system.read(&known_hosts).unwrap_or_default();
    if String::from_utf8_lossy(&known).contains("github.com") {
        return;
    }
    let scan = Command::new("ssh-keyscan", ["-t", "ed25519", "github.com"])
        .timeout(Duration::from_secs(15));
    if let Ok(output) = system.run(&scan)
        && output.success()
        && !output.stdout.is_empty()
    {
        // Without the key the clone over SSH fails and HTTPS is tried.
        let _ = system.append(&known_hosts, &output.stdout, 0o644);
    }
}

fn base64(data: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for chunk in data.chunks(3) {
        let group = chunk
            .iter()
            .enumerate()
            .fold(0u32, |acc, (i, byte)| acc | u32::from(*byte) << (16 - 8 * i));
        for position in 0..4 {
            if position <= chunk.len() {
                out.push(char::from(ALPHABET[(group >> (18 - 6 * position) & 63) as usize]));
            } else {
                out.push('=');
            }
        }
    }
    out
}

/// Clones a repository into `dir`: over SSH, and when that fails over HTTPS with the token.
/// `Err` is why neither worked.
pub(crate) fn clone(
    system: &dyn System,
    home: &Path,
    remote: &Remote,
    token: &str,
    dir: &Path,
) -> Result<(), String> {
    const LIMIT: Duration = Duration::from_secs(600);
    let target = dir.to_string_lossy();
    if remote.ssh_url.contains("github.com") {
        know_github(system, home);
    }
    let over_ssh = git::command(&["clone", "--quiet", &remote.ssh_url, &target]).timeout(LIMIT);
    let first = match system.run(&over_ssh) {
        Ok(output) if output.success() => return Ok(()),
        Ok(output) => one_line(&output.error_text(), 200),
        Err(error) => return Err(format!("couldn't run git: {error}")),
    };
    if remote.https_url.is_empty() {
        return Err(first);
    }
    // The token is given to this one git through its environment: it is not written into the
    // clone's configuration and is not on a command line.
    let credentials = base64(format!("x-access-token:{token}").as_bytes());
    let over_https = git::command(&["clone", "--quiet", &remote.https_url, &target])
        .env("GIT_CONFIG_COUNT", "1")
        .env("GIT_CONFIG_KEY_0", "http.extraHeader")
        .env("GIT_CONFIG_VALUE_0", &format!("Authorization: Basic {credentials}"))
        .timeout(LIMIT);
    match system.run(&over_https) {
        Ok(output) if output.success() => Ok(()),
        Ok(output) => Err(one_line(&output.error_text(), 200)),
        Err(error) => Err(format!("couldn't run git: {error}")),
    }
}

#[cfg(test)]
mod tests {
    use super::base64;

    #[test]
    fn base64_pads() {
        assert_eq!(base64(b""), "");
        assert_eq!(base64(b"f"), "Zg==");
        assert_eq!(base64(b"fo"), "Zm8=");
        assert_eq!(base64(b"foo"), "Zm9v");
        assert_eq!(base64(b"x-access-token:FAKE"), "eC1hY2Nlc3MtdG9rZW46RkFLRQ==");
    }
}
