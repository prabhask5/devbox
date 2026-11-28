#![allow(clippy::redundant_closure_call)]
#![allow(clippy::needless_lifetimes)]
#![allow(clippy::match_single_binding)]
#![allow(clippy::clone_on_copy)]

#[doc = "One kind of agent."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct AdapterInfo {
    #[doc = "Buttons shown when a session of this agent needs input."]
    #[serde(rename = "answerKeys")]
    pub answer_keys: ::std::vec::Vec<AnswerKey>,
    #[doc = "Its binary is installed."]
    pub available: bool,
    #[doc = "It reports lifecycle events."]
    pub hooks: bool,
    #[doc = "Adapter id, e.g. `claude`."]
    pub id: ::std::string::String,
    #[doc = "How the hub talks to it: a terminal in tmux, or a chat API (reserved)."]
    pub kind: AdapterInfoKind,
    #[doc = "Full name, e.g. `Claude Code`."]
    pub label: ::std::string::String,
    #[doc = "Short name, e.g. `Claude`."]
    pub short: ::std::string::String,
}
#[doc = "How the hub talks to it: a terminal in tmux, or a chat API (reserved)."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum AdapterInfoKind {
    #[serde(rename = "tmux")]
    Tmux,
    #[serde(rename = "chat")]
    Chat,
}
impl ::std::fmt::Display for AdapterInfoKind {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Tmux => f.write_str("tmux"),
            Self::Chat => f.write_str("chat"),
        }
    }
}
impl ::std::str::FromStr for AdapterInfoKind {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "tmux" => Ok(Self::Tmux),
            "chat" => Ok(Self::Chat),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for AdapterInfoKind {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for AdapterInfoKind {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "One lifecycle event of a coding agent, sent by the hook relay to the hub over the internal socket."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct AgentEvent {
    #[doc = "Adapter id, e.g. `claude`."]
    pub agent: ::std::string::String,
    #[doc = "Working directory of the agent."]
    pub cwd: ::std::string::String,
    #[doc = "`$DEVBOX_SESSION` of the agent's process, when the hub started it."]
    #[serde(rename = "devboxSession", skip_serializing_if = "::std::option::Option::is_none")]
    pub devbox_session: ::std::option::Option<::std::string::String>,
    #[doc = "What happened. `Notification` is Claude's own event, which the hub maps onto the others."]
    pub event: AgentEventEvent,
    #[doc = "The agent's hook JSON as received; a string when it was not valid JSON."]
    pub payload: ::serde_json::Value,
    #[doc = "`$TMUX_PANE` of the agent's process, when it runs in tmux."]
    #[serde(rename = "tmuxPane", skip_serializing_if = "::std::option::Option::is_none")]
    pub tmux_pane: ::std::option::Option<::std::string::String>,
    #[doc = "When the event happened (Unix time, ms)."]
    pub ts: i64,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "What happened. `Notification` is Claude's own event, which the hub maps onto the others."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum AgentEventEvent {
    SessionStart,
    UserPromptSubmit,
    PreToolUse,
    PostToolUse,
    PermissionRequest,
    Notification,
    Stop,
    Error,
    SessionEnd,
}
impl ::std::fmt::Display for AgentEventEvent {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::SessionStart => f.write_str("SessionStart"),
            Self::UserPromptSubmit => f.write_str("UserPromptSubmit"),
            Self::PreToolUse => f.write_str("PreToolUse"),
            Self::PostToolUse => f.write_str("PostToolUse"),
            Self::PermissionRequest => f.write_str("PermissionRequest"),
            Self::Notification => f.write_str("Notification"),
            Self::Stop => f.write_str("Stop"),
            Self::Error => f.write_str("Error"),
            Self::SessionEnd => f.write_str("SessionEnd"),
        }
    }
}
impl ::std::str::FromStr for AgentEventEvent {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "SessionStart" => Ok(Self::SessionStart),
            "UserPromptSubmit" => Ok(Self::UserPromptSubmit),
            "PreToolUse" => Ok(Self::PreToolUse),
            "PostToolUse" => Ok(Self::PostToolUse),
            "PermissionRequest" => Ok(Self::PermissionRequest),
            "Notification" => Ok(Self::Notification),
            "Stop" => Ok(Self::Stop),
            "Error" => Ok(Self::Error),
            "SessionEnd" => Ok(Self::SessionEnd),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for AgentEventEvent {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for AgentEventEvent {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "A button that sends one key to a session."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct AnswerKey {
    #[doc = "tmux key name, e.g. `Enter` or `Escape`."]
    pub key: ::std::string::String,
    #[doc = "Text on the button."]
    pub label: ::std::string::String,
}
#[doc = "The last self-update of the hub."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct DeployState {
    #[doc = "When the update finished (Unix time, ms)."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub at: ::std::option::Option<i64>,
    #[doc = "Why it failed."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub error: ::std::option::Option<::std::string::String>,
    #[doc = "How the last update went."]
    pub status: DeployStateStatus,
    #[doc = "Commit the update installs or installed."]
    pub version: ::std::string::String,
}
#[doc = "How the last update went."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum DeployStateStatus {
    #[serde(rename = "success")]
    Success,
    #[serde(rename = "failure")]
    Failure,
    #[serde(rename = "in_progress")]
    InProgress,
}
impl ::std::fmt::Display for DeployStateStatus {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Success => f.write_str("success"),
            Self::Failure => f.write_str("failure"),
            Self::InProgress => f.write_str("in_progress"),
        }
    }
}
impl ::std::str::FromStr for DeployStateStatus {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "success" => Ok(Self::Success),
            "failure" => Ok(Self::Failure),
            "in_progress" => Ok(Self::InProgress),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for DeployStateStatus {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for DeployStateStatus {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "`devbox.toml` at the root of a dotfiles repository: what devbox installs from it. Paths are relative to the repository root."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct DotfilesManifest {
    pub agents: DotfilesManifestAgents,
    pub code_server: DotfilesManifestCodeServer,
    pub context: DotfilesManifestContext,
    pub shell: DotfilesManifestShell,
    pub sync: DotfilesManifestSync,
    #[doc = "Version of this file's format."]
    pub version: i64,
    pub vm: DotfilesManifestVm,
}
#[doc = "Agent instructions."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, Default, PartialEq)]
pub struct DotfilesManifestAgents {
    #[doc = "Instructions every agent reads."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub instructions: ::std::option::Option<::std::string::String>,
}
#[doc = "Editor configuration."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, Default, PartialEq)]
pub struct DotfilesManifestCodeServer {
    #[doc = "File with one Open VSX extension id per line."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub extensions: ::std::option::Option<::std::string::String>,
    #[doc = "keybindings.json linked into code-server's User directory."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub keybindings: ::std::option::Option<::std::string::String>,
    #[doc = "settings.json linked into code-server's User directory."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub settings: ::std::option::Option<::std::string::String>,
}
#[doc = "Generated developer context."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct DotfilesManifestContext {
    #[doc = "Write an AI summary."]
    pub ai: bool,
    #[doc = "Commit the context directory after each run."]
    pub commit: bool,
    #[doc = "Directory of the dotfiles `devbox context` writes to. Absent: `context`."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub dir: ::std::option::Option<::std::string::String>,
}
#[doc = "Shell and git configuration."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, Default, PartialEq)]
pub struct DotfilesManifestShell {
    #[doc = "File sourced from ~/.bashrc."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub bashrc: ::std::option::Option<::std::string::String>,
    #[doc = "File included from ~/.gitconfig."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub gitconfig: ::std::option::Option<::std::string::String>,
}
#[doc = "Which repositories are cloned and synced."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct DotfilesManifestSync {
    #[doc = "Include archived repositories."]
    pub archived: bool,
    #[doc = "Globs on repository names to leave out."]
    pub exclude: ::std::vec::Vec<::std::string::String>,
    #[doc = "Extra owners or organizations."]
    pub include_orgs: ::std::vec::Vec<::std::string::String>,
    #[doc = "Whose repositories to clone. Absent: the authenticated GitHub user."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub owner: ::std::option::Option<::std::string::String>,
}
#[doc = "Additions to the VM."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct DotfilesManifestVm {
    #[doc = "Script run as the VM user after provisioning; must be idempotent."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub install: ::std::option::Option<::std::string::String>,
    #[doc = "apt packages on top of devbox's base set."]
    pub packages: ::std::vec::Vec<::std::string::String>,
}
#[doc = "Health of the devbox for monitors, served at /__devbox/health (200 when ok, 503 otherwise)."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct GatewayHealth {
    #[doc = "When this was computed (Unix time, ms)."]
    #[serde(rename = "checkedAt")]
    pub checked_at: i64,
    pub phase: GatewayPhase,
    #[doc = "One stable sentence per problem."]
    pub problems: ::std::vec::Vec<::std::string::String>,
    #[doc = "`ok`, or `problem` when `problems` is not empty."]
    pub status: GatewayHealthStatus,
    #[doc = "Contract version of this message."]
    pub v: i64,
    pub vm: VmState,
}
#[doc = "`ok`, or `problem` when `problems` is not empty."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum GatewayHealthStatus {
    #[serde(rename = "ok")]
    Ok,
    #[serde(rename = "problem")]
    Problem,
}
impl ::std::fmt::Display for GatewayHealthStatus {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Ok => f.write_str("ok"),
            Self::Problem => f.write_str("problem"),
        }
    }
}
impl ::std::str::FromStr for GatewayHealthStatus {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "ok" => Ok(Self::Ok),
            "problem" => Ok(Self::Problem),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for GatewayHealthStatus {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for GatewayHealthStatus {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "How long the devbox has been idle and why."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct GatewayIdle {
    #[doc = "Time since the last activity through the gateway, in ms."]
    #[serde(rename = "gatewayIdleMs")]
    pub gateway_idle_ms: i64,
    #[doc = "Time the hub reports being idle, in ms; absent when the hub is unreachable."]
    #[serde(rename = "hubIdleMs", skip_serializing_if = "::std::option::Option::is_none")]
    pub hub_idle_ms: ::std::option::Option<i64>,
    #[doc = "Why the hub is busy, if it is."]
    #[serde(rename = "hubReasons")]
    pub hub_reasons: ::std::vec::Vec<::std::string::String>,
    #[doc = "The smaller of the gateway's and the hub's idle time, in ms."]
    #[serde(rename = "idleMs")]
    pub idle_ms: i64,
    #[doc = "Idle time after which the VM sleeps, in ms."]
    #[serde(rename = "limitMs")]
    pub limit_ms: i64,
}
#[doc = "What the gateway is doing with the VM."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum GatewayPhase {
    #[serde(rename = "idle")]
    Idle,
    #[serde(rename = "waking")]
    Waking,
    #[serde(rename = "stopping")]
    Stopping,
}
impl ::std::fmt::Display for GatewayPhase {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Idle => f.write_str("idle"),
            Self::Waking => f.write_str("waking"),
            Self::Stopping => f.write_str("stopping"),
        }
    }
}
impl ::std::str::FromStr for GatewayPhase {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "idle" => Ok(Self::Idle),
            "waking" => Ok(Self::Waking),
            "stopping" => Ok(Self::Stopping),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for GatewayPhase {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for GatewayPhase {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "When each paired SSH key was last used, kept by the gateway in its state directory (`ssh/keys-used.json`) and read by `devbox ssh keys`."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct GatewaySshKeysUsed {
    #[doc = "Last use (Unix time, ms) by key fingerprint (`SHA256:…`)."]
    pub keys: ::std::collections::BTreeMap<::std::string::String, i64>,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "State of the gateway and the VM, served at /__devbox/status and pushed over /__devbox/events."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct GatewayStatus {
    #[doc = "Whether this request carries a valid wake cookie. Only on /__devbox/status."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub authed: ::std::option::Option<bool>,
    #[doc = "False until `devbox up` has written the host config."]
    pub configured: bool,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub idle: ::std::option::Option<GatewayIdle>,
    #[serde(rename = "lastStop", skip_serializing_if = "::std::option::Option::is_none")]
    pub last_stop: ::std::option::Option<LastStop>,
    pub phase: GatewayPhase,
    #[doc = "The gateway's clock, so pages can compute elapsed times (Unix time, ms)."]
    #[serde(rename = "serverNow")]
    pub server_now: i64,
    pub typical: GatewayStatusTypical,
    #[doc = "Contract version of this message."]
    pub v: i64,
    #[doc = "Commit the gateway was built from."]
    pub version: ::std::string::String,
    pub vm: VmState,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub wake: ::std::option::Option<Wake>,
}
#[doc = "Typical phase durations, from past wakes."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct GatewayStatusTypical {
    pub cold: WakeEstimate,
    pub restore: WakeEstimate,
}
#[doc = "Host configuration, /etc/devbox/devbox.toml: written by `devbox up`, read by the CLI and the gateway."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfig {
    pub deploy: HostConfigDeploy,
    #[doc = "Domain the devbox is served under."]
    pub domain: ::std::string::String,
    pub firewall: HostConfigFirewall,
    pub gateway: HostConfigGateway,
    pub hosts: HostConfigHosts,
    #[doc = "Incus instance name."]
    pub instance: ::std::string::String,
    pub provision: HostConfigProvision,
    pub routing: HostConfigRouting,
    pub sleep: HostConfigSleep,
    #[doc = "Version of this file's format."]
    pub version: i64,
    pub vm: HostConfigVm,
}
#[doc = "Continuous deployment of the hub."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfigDeploy {
    #[doc = "GitHub environment of the hub's deployments."]
    pub hub_environment: ::std::string::String,
    #[doc = "GitHub repository whose deployments the VM installs. Absent: the hub does not update itself."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub repo: ::std::option::Option<::std::string::String>,
    #[doc = "Wake a sleeping VM to install a new hub build."]
    pub wake_to_deploy: bool,
}
#[doc = "What puts the firewall rule for the VM in force."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, Default, PartialEq)]
pub struct HostConfigFirewall {
    #[doc = "The firewall manager `devbox up` uses: `host-firewall`, `ufw`, `nftables`, `moi`, `none`, or the name of an embedder's own. Absent: `devbox up` finds out which."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub manager: ::std::option::Option<::std::string::String>,
}
#[doc = "Gateway listeners and timing."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfigGateway {
    #[doc = "HTTP listen address."]
    pub listen: ::std::string::String,
    #[doc = "SSH proxy listen address; empty disables SSH proxying."]
    pub ssh_listen: ::std::string::String,
    #[doc = "How long a wake waits for the repo sync before going on without it."]
    pub sync_wait: ::std::string::String,
}
#[doc = "Public hostnames."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, Default, PartialEq)]
pub struct HostConfigHosts {
    #[doc = "Agents app hostname. Default: `agents.<domain>`."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub agents: ::std::option::Option<::std::string::String>,
    #[doc = "Editor hostname. Default: `code.<domain>`."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub code: ::std::option::Option<::std::string::String>,
    #[doc = "SSH hostname (SSH over WebSocket, the setup script and pairing). Default: `devbox.<domain>`."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub ssh: ::std::option::Option<::std::string::String>,
}
#[doc = "How `devbox up` built the VM, so that it can build it again without being asked anything."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfigProvision {
    #[doc = "Coding agents installed in the VM."]
    pub agents: ::std::vec::Vec<::std::string::String>,
    #[doc = "vCPUs of the VM."]
    pub cpu: i64,
    #[doc = "Root disk of the VM, like `60GiB`."]
    pub disk: ::std::string::String,
    #[doc = "The owner's dotfiles repository, cloned into the VM and applied."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub dotfiles: ::std::option::Option<::std::string::String>,
    #[doc = "Image the VM is created from, as `remote:alias`."]
    pub image: ::std::string::String,
    #[doc = "Memory of the VM, like `5GiB`."]
    pub memory: ::std::string::String,
    #[doc = "The user in the VM that owns the repositories and the agent sessions. Absent until `devbox up` has run."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub user: ::std::option::Option<::std::string::String>,
}
#[doc = "How public traffic reaches the gateway."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfigRouting {
    #[doc = "Traefik entrypoint for the routes."]
    pub entrypoint: ::std::string::String,
    #[doc = "The Traefik dynamic file devbox owns."]
    pub file: ::std::string::String,
    #[doc = "`traefik-file` writes a Traefik dynamic file; `none` leaves routing to the owner."]
    pub provider: HostConfigRoutingProvider,
    #[doc = "URL Traefik uses to reach the gateway."]
    pub service_url: ::std::string::String,
}
#[doc = "`traefik-file` writes a Traefik dynamic file; `none` leaves routing to the owner."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum HostConfigRoutingProvider {
    #[serde(rename = "traefik-file")]
    TraefikFile,
    #[serde(rename = "none")]
    None,
}
impl ::std::fmt::Display for HostConfigRoutingProvider {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::TraefikFile => f.write_str("traefik-file"),
            Self::None => f.write_str("none"),
        }
    }
}
impl ::std::str::FromStr for HostConfigRoutingProvider {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "traefik-file" => Ok(Self::TraefikFile),
            "none" => Ok(Self::None),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for HostConfigRoutingProvider {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for HostConfigRoutingProvider {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "When the VM is put to sleep."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfigSleep {
    #[doc = "Client bytes per minute that count as activity."]
    pub activity_bytes_per_min: u64,
    #[doc = "Idle time after which the VM sleeps."]
    pub idle: ::std::string::String,
    #[doc = "Minimum time the VM stays up after a wake."]
    pub min_up: ::std::string::String,
    #[doc = "`stateful` saves memory, `shutdown` stops cleanly, `off` never sleeps."]
    pub mode: HostConfigSleepMode,
}
#[doc = "`stateful` saves memory, `shutdown` stops cleanly, `off` never sleeps."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum HostConfigSleepMode {
    #[serde(rename = "stateful")]
    Stateful,
    #[serde(rename = "shutdown")]
    Shutdown,
    #[serde(rename = "off")]
    Off,
}
impl ::std::fmt::Display for HostConfigSleepMode {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Stateful => f.write_str("stateful"),
            Self::Shutdown => f.write_str("shutdown"),
            Self::Off => f.write_str("off"),
        }
    }
}
impl ::std::str::FromStr for HostConfigSleepMode {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "stateful" => Ok(Self::Stateful),
            "shutdown" => Ok(Self::Shutdown),
            "off" => Ok(Self::Off),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for HostConfigSleepMode {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for HostConfigSleepMode {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "How the gateway reaches the VM."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostConfigVm {
    #[doc = "Static address of the VM on the Incus bridge."]
    pub address: ::std::net::Ipv4Addr,
    #[doc = "code-server port."]
    pub code_port: ::std::num::NonZeroU16,
    #[doc = "Hub port."]
    pub hub_port: ::std::num::NonZeroU16,
    #[doc = "The VM's SSH host key, as one `ssh-ed25519 AAAA…` line, recorded by `devbox up`. The gateway connects to no other. Absent until `devbox up` has run."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub ssh_host_key: ::std::option::Option<::std::string::String>,
    #[doc = "sshd port."]
    pub ssh_port: ::std::num::NonZeroU16,
}
#[doc = "Host secrets, /etc/devbox/secrets.toml (mode 0600)."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HostSecrets {
    #[doc = "argon2id hash of the agents app's password, kept here too so that a rebuilt VM gets it back without anyone being asked. The gateway does not read it."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub app_password_hash: ::std::option::Option<::std::string::String>,
    #[doc = "Key that signs wake cookies."]
    pub cookie_secret: ::devbox_core::secret::Secret,
    #[doc = "Bearer token the gateway presents to the hub."]
    pub hub_token: ::devbox_core::secret::Secret,
    #[doc = "Version of this file's format."]
    pub version: i64,
    #[doc = "argon2id hash (PHC string) of the password that wakes the VM."]
    pub wake_password_hash: ::devbox_core::secret::Secret,
}
#[doc = "Hub configuration, ~/.config/devbox/hub.toml in the VM."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubConfig {
    pub deploy: HubConfigDeploy,
    pub hosts: HubConfigHosts,
    #[doc = "Listen address for the app and the gateway API."]
    pub listen: ::std::string::String,
    pub tmux: HubConfigTmux,
    #[doc = "Version of this file's format."]
    pub version: i64,
}
#[doc = "Self-update from GitHub Deployments."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubConfigDeploy {
    #[doc = "GitHub environment of the hub's deployments."]
    pub environment: ::std::string::String,
    #[doc = "GitHub repository whose deployments the VM installs. Absent: the hub does not update itself."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub repo: ::std::option::Option<::std::string::String>,
}
#[doc = "Public hostnames, for links the hub hands out."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubConfigHosts {
    #[doc = "Agents app hostname."]
    pub agents: ::std::string::String,
    #[doc = "Editor hostname."]
    pub code: ::std::string::String,
}
#[doc = "The tmux server that holds agent sessions."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubConfigTmux {
    #[doc = "tmux socket name (`tmux -L`)."]
    pub socket: ::std::string::String,
}
#[doc = "Whether anything is keeping the VM busy."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubIdle {
    #[doc = "Something is running that must not be interrupted by sleep."]
    pub busy: bool,
    #[doc = "Seconds since the hub was last busy, from a monotonic clock."]
    #[serde(rename = "idleSeconds")]
    pub idle_seconds: i64,
    #[doc = "One short sentence per reason the hub is busy."]
    pub reasons: ::std::vec::Vec<::std::string::String>,
}
#[doc = "Hub secrets, ~/.config/devbox/hub.secrets.toml in the VM (mode 0600)."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubSecrets {
    #[doc = "argon2id hash (PHC string) of the agents app password."]
    pub app_password_hash: ::devbox_core::secret::Secret,
    #[doc = "Key that signs app session cookies."]
    pub cookie_secret: ::devbox_core::secret::Secret,
    #[doc = "Token for reading deployments and reporting their status."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub github_token: ::std::option::Option<::devbox_core::secret::Secret>,
    #[doc = "Bearer token the gateway must present."]
    pub hub_token: ::devbox_core::secret::Secret,
    #[doc = "Web push private key, base64url; written by the hub on first run."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub vapid_private_key: ::std::option::Option<::devbox_core::secret::Secret>,
    #[doc = "Web push application server key, base64url; written by the hub on first run."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub vapid_public_key: ::std::option::Option<::std::string::String>,
    #[doc = "Version of this file's format."]
    pub version: i64,
}
#[doc = "Everything the agents app shows, pushed over /api/stream on every change."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubState {
    #[doc = "The agents the hub knows."]
    pub adapters: ::std::vec::Vec<AdapterInfo>,
    #[doc = "Keys offered for every terminal session."]
    #[serde(rename = "commonKeys")]
    pub common_keys: ::std::vec::Vec<AnswerKey>,
    pub devbox: HubStatus,
    #[doc = "Devices subscribed to notifications."]
    #[serde(rename = "pushDevices")]
    pub push_devices: i64,
    #[doc = "The hub's clock (Unix time, ms)."]
    #[serde(rename = "serverNow")]
    pub server_now: i64,
    #[doc = "Every session, most recently changed first."]
    pub sessions: ::std::vec::Vec<Session>,
    #[doc = "Contract version of this message."]
    pub v: i64,
    #[doc = "Web push application server key, base64url."]
    #[serde(rename = "vapidPublicKey", skip_serializing_if = "::std::option::Option::is_none")]
    pub vapid_public_key: ::std::option::Option<::std::string::String>,
}
#[doc = "What the hub tells the gateway at GET /devbox/status: whether the VM may sleep, sync progress, versions."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubStatus {
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub deploy: ::std::option::Option<DeployState>,
    pub idle: HubIdle,
    pub sync: SyncState,
    #[doc = "Seconds since the hub started."]
    pub uptime: i64,
    #[doc = "Contract version of this message."]
    pub v: i64,
    #[doc = "Commit the hub was built from."]
    pub version: ::std::string::String,
}
#[doc = "What the gateway posts to the hub (POST /devbox/wake) once the VM is up."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubWake {
    pub kind: WakeKind,
    #[doc = "The host's clock, to correct the guest clock after a restore (Unix time, ms)."]
    pub now: i64,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "The hub's answer to POST /devbox/wake."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct HubWakeResponse {
    #[doc = "The sync run to wait for."]
    #[serde(rename = "runId")]
    pub run_id: ::std::string::String,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "One sleep of the VM."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct LastStop {
    #[doc = "When the VM stopped (Unix time, ms)."]
    pub at: i64,
    #[doc = "How: `stateful`, `shutdown`, or `shutdown (stateful failed)`."]
    pub mode: ::std::string::String,
    #[doc = "How long stopping took, in ms."]
    pub ms: i64,
    #[doc = "Why, e.g. `idle 15m`."]
    pub reason: ::std::string::String,
}
#[doc = "Milliseconds per wake phase."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct PhaseDurations {
    #[doc = "The `code` phase."]
    pub code: i64,
    #[doc = "The `hub` phase."]
    pub hub: i64,
    #[doc = "The `start` phase."]
    pub start: i64,
    #[doc = "The `sync` phase."]
    pub sync: i64,
}
#[doc = "When one wake phase ran."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct PhaseTiming {
    #[doc = "When it finished; absent while running (Unix time, ms)."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub end: ::std::option::Option<i64>,
    #[doc = "The phase timed out and the wake went on without it."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub skipped: ::std::option::Option<bool>,
    #[doc = "When the phase began (Unix time, ms)."]
    pub start: i64,
}
#[doc = "A notification as the service worker receives it."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct PushPayload {
    #[doc = "App badge count: sessions that need input or are unread."]
    pub badge: i64,
    #[doc = "Notification text."]
    pub body: ::std::string::String,
    #[doc = "Notifications with the same tag replace each other."]
    pub tag: ::std::string::String,
    #[doc = "Notification title."]
    pub title: ::std::string::String,
    #[doc = "Where a tap goes, e.g. `/#/s/a1b2c3d4`."]
    pub url: ::std::string::String,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "One frame of a session's screen, pushed over /api/sessions/:id/screen."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct ScreenFrame {
    #[doc = "Columns of the tmux window."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub cols: ::std::option::Option<i64>,
    #[doc = "Why there is no screen."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub reason: ::std::option::Option<ScreenFrameReason>,
    #[doc = "Rows of the tmux window."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub rows: ::std::option::Option<i64>,
    #[doc = "Scrollback and screen as lines with ANSI SGR styling; absent when there is no screen to show."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub text: ::std::option::Option<::std::string::String>,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "Why there is no screen."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum ScreenFrameReason {
    #[serde(rename = "external")]
    External,
    #[serde(rename = "gone")]
    Gone,
}
impl ::std::fmt::Display for ScreenFrameReason {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::External => f.write_str("external"),
            Self::Gone => f.write_str("gone"),
        }
    }
}
impl ::std::str::FromStr for ScreenFrameReason {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "external" => Ok(Self::External),
            "gone" => Ok(Self::Gone),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for ScreenFrameReason {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for ScreenFrameReason {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "One agent session: a tmux session (or an agent seen only through its hooks) and what the hub knows about it."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct Session {
    #[doc = "The tmux session was not created by the hub."]
    pub adopted: bool,
    #[doc = "Adapter id."]
    pub agent: ::std::string::String,
    #[doc = "The agent's own session id."]
    #[serde(rename = "agentSessionId", skip_serializing_if = "::std::option::Option::is_none")]
    pub agent_session_id: ::std::option::Option<::std::string::String>,
    #[doc = "A tmux client is attached."]
    pub attached: bool,
    #[doc = "When the session was created or first seen (Unix time, ms)."]
    #[serde(rename = "createdAt")]
    pub created_at: i64,
    #[doc = "Working directory."]
    pub cwd: ::std::string::String,
    #[doc = "Eight hex characters."]
    pub id: ::std::string::String,
    #[doc = "`tmux`: a terminal the hub can show and type into. `external`: seen only through hook events. `chat`: reserved."]
    pub kind: SessionKind,
    #[doc = "When the last hook event arrived (Unix time, ms)."]
    #[serde(rename = "lastEventAt", skip_serializing_if = "::std::option::Option::is_none")]
    pub last_event_at: ::std::option::Option<i64>,
    #[doc = "The owner's last prompt."]
    #[serde(rename = "lastPrompt", skip_serializing_if = "::std::option::Option::is_none")]
    pub last_prompt: ::std::option::Option<::std::string::String>,
    #[doc = "The agent's last reply."]
    #[serde(rename = "lastReply", skip_serializing_if = "::std::option::Option::is_none")]
    pub last_reply: ::std::option::Option<::std::string::String>,
    #[doc = "The tool the agent last used."]
    #[serde(rename = "lastTool", skip_serializing_if = "::std::option::Option::is_none")]
    pub last_tool: ::std::option::Option<::std::string::String>,
    #[doc = "Unique display name, also the tmux session name when created by the hub."]
    pub name: ::std::string::String,
    #[doc = "Repository name when `cwd` is inside `~/code/<repo>`."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub repo: ::std::option::Option<::std::string::String>,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub size: ::std::option::Option<SessionSize>,
    pub state: SessionState,
    #[doc = "When `state` last changed (Unix time, ms)."]
    #[serde(rename = "stateSince")]
    pub state_since: i64,
    #[doc = "Short description, from the first prompt; may be empty."]
    pub title: ::std::string::String,
    #[doc = "tmux session name; absent for external sessions."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub tmux: ::std::option::Option<::std::string::String>,
    #[doc = "Where the agent keeps its transcript."]
    #[serde(rename = "transcriptPath", skip_serializing_if = "::std::option::Option::is_none")]
    pub transcript_path: ::std::option::Option<::std::string::String>,
    #[doc = "When the current turn started (Unix time, ms)."]
    #[serde(rename = "turnStartedAt", skip_serializing_if = "::std::option::Option::is_none")]
    pub turn_started_at: ::std::option::Option<i64>,
    #[doc = "Something happened that the owner has not looked at."]
    pub unread: bool,
    #[doc = "Contract version of this message."]
    pub v: i64,
    #[doc = "Open screen streams from the app."]
    pub viewers: i64,
    #[doc = "What the agent is asking, while `state` is `needs_input`."]
    #[serde(rename = "waitingFor", skip_serializing_if = "::std::option::Option::is_none")]
    pub waiting_for: ::std::option::Option<::std::string::String>,
}
#[doc = "`tmux`: a terminal the hub can show and type into. `external`: seen only through hook events. `chat`: reserved."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum SessionKind {
    #[serde(rename = "tmux")]
    Tmux,
    #[serde(rename = "external")]
    External,
    #[serde(rename = "chat")]
    Chat,
}
impl ::std::fmt::Display for SessionKind {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Tmux => f.write_str("tmux"),
            Self::External => f.write_str("external"),
            Self::Chat => f.write_str("chat"),
        }
    }
}
impl ::std::str::FromStr for SessionKind {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "tmux" => Ok(Self::Tmux),
            "external" => Ok(Self::External),
            "chat" => Ok(Self::Chat),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for SessionKind {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for SessionKind {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "Size of the tmux window."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct SessionSize {
    #[doc = "Columns."]
    pub cols: i64,
    #[doc = "Rows."]
    pub rows: i64,
}
#[doc = "Where a session is in its lifecycle."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum SessionState {
    #[serde(rename = "starting")]
    Starting,
    #[serde(rename = "working")]
    Working,
    #[serde(rename = "needs_input")]
    NeedsInput,
    #[serde(rename = "idle")]
    Idle,
    #[serde(rename = "exited")]
    Exited,
    #[serde(rename = "error")]
    Error,
    #[serde(rename = "gone")]
    Gone,
    #[serde(rename = "ended")]
    Ended,
}
impl ::std::fmt::Display for SessionState {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Starting => f.write_str("starting"),
            Self::Working => f.write_str("working"),
            Self::NeedsInput => f.write_str("needs_input"),
            Self::Idle => f.write_str("idle"),
            Self::Exited => f.write_str("exited"),
            Self::Error => f.write_str("error"),
            Self::Gone => f.write_str("gone"),
            Self::Ended => f.write_str("ended"),
        }
    }
}
impl ::std::str::FromStr for SessionState {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "starting" => Ok(Self::Starting),
            "working" => Ok(Self::Working),
            "needs_input" => Ok(Self::NeedsInput),
            "idle" => Ok(Self::Idle),
            "exited" => Ok(Self::Exited),
            "error" => Ok(Self::Error),
            "gone" => Ok(Self::Gone),
            "ended" => Ok(Self::Ended),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for SessionState {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for SessionState {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "One line of `devbox sync --json` (NDJSON). `start` carries `total`; `begin` carries `name`; `repo` carries `name`, `status` and `detail`; `done` ends the run."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct SyncProgress {
    #[doc = "One line for the owner."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub detail: ::std::option::Option<::std::string::String>,
    #[doc = "Repository directory name, or `fetching` while all repositories are fetched."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub name: ::std::option::Option<::std::string::String>,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub status: ::std::option::Option<SyncStatus>,
    #[doc = "Repositories in this run."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub total: ::std::option::Option<i64>,
    #[doc = "What this line reports."]
    #[serde(rename = "type")]
    pub type_: SyncProgressType,
    #[doc = "Contract version of this message."]
    pub v: i64,
}
#[doc = "What this line reports."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum SyncProgressType {
    #[serde(rename = "start")]
    Start,
    #[serde(rename = "begin")]
    Begin,
    #[serde(rename = "repo")]
    Repo,
    #[serde(rename = "done")]
    Done,
}
impl ::std::fmt::Display for SyncProgressType {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Start => f.write_str("start"),
            Self::Begin => f.write_str("begin"),
            Self::Repo => f.write_str("repo"),
            Self::Done => f.write_str("done"),
        }
    }
}
impl ::std::str::FromStr for SyncProgressType {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "start" => Ok(Self::Start),
            "begin" => Ok(Self::Begin),
            "repo" => Ok(Self::Repo),
            "done" => Ok(Self::Done),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for SyncProgressType {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for SyncProgressType {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "Outcome of syncing one repository."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct SyncResult {
    #[doc = "One line for the owner, e.g. `pulled 3`."]
    pub detail: ::std::string::String,
    #[doc = "Repository directory name."]
    pub name: ::std::string::String,
    pub status: SyncStatus,
}
#[doc = "Progress of a repo sync run."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct SyncState {
    #[doc = "Repository being synced now."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub current: ::std::option::Option<::std::string::String>,
    #[doc = "Repositories finished so far."]
    pub done: i64,
    #[doc = "When the run finished (Unix time, ms)."]
    #[serde(rename = "finishedAt", skip_serializing_if = "::std::option::Option::is_none")]
    pub finished_at: ::std::option::Option<i64>,
    #[doc = "What started the run, e.g. `wake (restore)`."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub reason: ::std::option::Option<::std::string::String>,
    #[doc = "Outcome per finished repository."]
    pub results: ::std::vec::Vec<SyncResult>,
    #[doc = "Identifies the run; absent before the first run."]
    #[serde(rename = "runId", skip_serializing_if = "::std::option::Option::is_none")]
    pub run_id: ::std::option::Option<::std::string::String>,
    #[doc = "When the run started (Unix time, ms)."]
    #[serde(rename = "startedAt", skip_serializing_if = "::std::option::Option::is_none")]
    pub started_at: ::std::option::Option<i64>,
    #[doc = "Whether a run is in progress."]
    pub state: SyncStateState,
    #[doc = "Repositories in this run."]
    pub total: i64,
}
#[doc = "Whether a run is in progress."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum SyncStateState {
    #[serde(rename = "idle")]
    Idle,
    #[serde(rename = "running")]
    Running,
    #[serde(rename = "done")]
    Done,
}
impl ::std::fmt::Display for SyncStateState {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Idle => f.write_str("idle"),
            Self::Running => f.write_str("running"),
            Self::Done => f.write_str("done"),
        }
    }
}
impl ::std::str::FromStr for SyncStateState {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "idle" => Ok(Self::Idle),
            "running" => Ok(Self::Running),
            "done" => Ok(Self::Done),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for SyncStateState {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for SyncStateState {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "Outcome of syncing one repository."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum SyncStatus {
    #[serde(rename = "current")]
    Current,
    #[serde(rename = "updated")]
    Updated,
    #[serde(rename = "cloned")]
    Cloned,
    #[serde(rename = "conflict")]
    Conflict,
    #[serde(rename = "skipped")]
    Skipped,
    #[serde(rename = "error")]
    Error,
}
impl ::std::fmt::Display for SyncStatus {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Current => f.write_str("current"),
            Self::Updated => f.write_str("updated"),
            Self::Cloned => f.write_str("cloned"),
            Self::Conflict => f.write_str("conflict"),
            Self::Skipped => f.write_str("skipped"),
            Self::Error => f.write_str("error"),
        }
    }
}
impl ::std::str::FromStr for SyncStatus {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "current" => Ok(Self::Current),
            "updated" => Ok(Self::Updated),
            "cloned" => Ok(Self::Cloned),
            "conflict" => Ok(Self::Conflict),
            "skipped" => Ok(Self::Skipped),
            "error" => Ok(Self::Error),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for SyncStatus {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for SyncStatus {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "State of the VM as the gateway sees it."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum VmState {
    #[serde(rename = "unknown")]
    Unknown,
    #[serde(rename = "stopped")]
    Stopped,
    #[serde(rename = "running")]
    Running,
    #[serde(rename = "error")]
    Error,
}
impl ::std::fmt::Display for VmState {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Unknown => f.write_str("unknown"),
            Self::Stopped => f.write_str("stopped"),
            Self::Running => f.write_str("running"),
            Self::Error => f.write_str("error"),
        }
    }
}
impl ::std::str::FromStr for VmState {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "unknown" => Ok(Self::Unknown),
            "stopped" => Ok(Self::Stopped),
            "running" => Ok(Self::Running),
            "error" => Ok(Self::Error),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for VmState {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for VmState {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "One wake of the VM."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct Wake {
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub current: ::std::option::Option<WakePhaseId>,
    #[doc = "The wake finished successfully."]
    pub done: bool,
    #[doc = "Why the wake failed."]
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub error: ::std::option::Option<::std::string::String>,
    pub eta: WakeEstimate,
    #[doc = "Identifies this wake, e.g. for the skip cookie."]
    pub id: ::std::string::String,
    pub kind: WakeKind,
    #[doc = "The last lines of the wake log."]
    pub log: ::std::vec::Vec<::std::string::String>,
    pub phases: WakePhases,
    #[doc = "What asked for the wake."]
    pub reason: ::std::string::String,
    #[doc = "When the wake was requested (Unix time, ms)."]
    #[serde(rename = "requestedAt")]
    pub requested_at: i64,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub sync: ::std::option::Option<SyncState>,
}
#[doc = "Expected phase durations for one kind of wake."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, PartialEq)]
pub struct WakeEstimate {
    pub phases: PhaseDurations,
    #[doc = "How many recorded wakes the medians come from."]
    pub samples: i64,
}
#[doc = "How the VM comes up: from saved memory, from a cold boot, or it was already running."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum WakeKind {
    #[serde(rename = "restore")]
    Restore,
    #[serde(rename = "cold")]
    Cold,
    #[serde(rename = "attach")]
    Attach,
}
impl ::std::fmt::Display for WakeKind {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Restore => f.write_str("restore"),
            Self::Cold => f.write_str("cold"),
            Self::Attach => f.write_str("attach"),
        }
    }
}
impl ::std::str::FromStr for WakeKind {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "restore" => Ok(Self::Restore),
            "cold" => Ok(Self::Cold),
            "attach" => Ok(Self::Attach),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for WakeKind {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for WakeKind {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "A phase of a wake."]
#[derive(
    :: serde :: Deserialize,
    :: serde :: Serialize,
    Clone,
    Copy,
    Debug,
    Eq,
    Hash,
    Ord,
    PartialEq,
    PartialOrd,
)]
pub enum WakePhaseId {
    #[serde(rename = "start")]
    Start,
    #[serde(rename = "code")]
    Code,
    #[serde(rename = "hub")]
    Hub,
    #[serde(rename = "sync")]
    Sync,
}
impl ::std::fmt::Display for WakePhaseId {
    fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
        match *self {
            Self::Start => f.write_str("start"),
            Self::Code => f.write_str("code"),
            Self::Hub => f.write_str("hub"),
            Self::Sync => f.write_str("sync"),
        }
    }
}
impl ::std::str::FromStr for WakePhaseId {
    type Err = self::error::ConversionError;
    fn from_str(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        match value {
            "start" => Ok(Self::Start),
            "code" => Ok(Self::Code),
            "hub" => Ok(Self::Hub),
            "sync" => Ok(Self::Sync),
            _ => Err("invalid value".into()),
        }
    }
}
impl ::std::convert::TryFrom<&str> for WakePhaseId {
    type Error = self::error::ConversionError;
    fn try_from(value: &str) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
impl ::std::convert::TryFrom<::std::string::String> for WakePhaseId {
    type Error = self::error::ConversionError;
    fn try_from(
        value: ::std::string::String,
    ) -> ::std::result::Result<Self, self::error::ConversionError> {
        value.parse()
    }
}
#[doc = "Timing of each phase started so far."]
#[derive(:: serde :: Deserialize, :: serde :: Serialize, Clone, Debug, Default, PartialEq)]
pub struct WakePhases {
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub code: ::std::option::Option<PhaseTiming>,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub hub: ::std::option::Option<PhaseTiming>,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub start: ::std::option::Option<PhaseTiming>,
    #[serde(skip_serializing_if = "::std::option::Option::is_none")]
    pub sync: ::std::option::Option<PhaseTiming>,
}
#[doc = " Error types."]
pub mod error {
    #[doc = r" Error from a `TryFrom` or `FromStr` implementation."]
    pub struct ConversionError(::std::borrow::Cow<'static, str>);
    impl ::std::error::Error for ConversionError {}
    impl ::std::fmt::Display for ConversionError {
        fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> Result<(), ::std::fmt::Error> {
            ::std::fmt::Display::fmt(&self.0, f)
        }
    }
    impl ::std::fmt::Debug for ConversionError {
        fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> Result<(), ::std::fmt::Error> {
            ::std::fmt::Debug::fmt(&self.0, f)
        }
    }
    impl From<&'static str> for ConversionError {
        fn from(value: &'static str) -> Self {
            Self(value.into())
        }
    }
    impl From<String> for ConversionError {
        fn from(value: String) -> Self {
            Self(value.into())
        }
    }
}
