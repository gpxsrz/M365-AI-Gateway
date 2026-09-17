#![doc = r##"
M365 AI Gateway owns the Model Provider / Transport Gateway surface only.
The standalone Agent-Control-Plane repository owns Task/Run governance and
semantic completion authority. The compile-fail examples below are deliberate
public-interface guards: exposing any of these local authority surfaces makes
the architecture test fail.

```compile_fail
use m365_ai_gateway::agent_ledger::AgentLedger;
```

```compile_fail
use m365_ai_gateway::governance::GovernanceStore;
```

```compile_fail
use m365_ai_gateway::task::Task;
```

```compile_fail
use m365_ai_gateway::run::Run;
```

```compile_fail
use m365_ai_gateway::completion::CompletionDecision;
```
"##]
#![recursion_limit = "256"]

pub mod admin;
mod agent_ledger;
pub mod api_keys;
mod artifact;
pub mod attachment;
pub mod auth;
mod browser_pkce;
mod catalog;
pub mod chathub;
pub mod checkpoint;
pub mod compat;
pub mod config;
mod debug;
mod deployments;
pub mod error;
mod hermes_attachments;
mod hindsight;
mod images;
mod mcp;
pub mod oauth_flow;
mod oauth_profiles;
pub mod private_file;
pub mod protocol;
pub mod runtime_settings;
pub mod tool_calls;
pub mod traffic;
pub mod web;

/// Compile-time architecture contract; this carries no lifecycle state.
#[doc(hidden)]
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum M365AuthoritySurface {
    ModelProviderTransport,
}

/// The only canonical authority surface this crate may own.
#[doc(hidden)]
pub const M365_AUTHORITY_SURFACE: M365AuthoritySurface =
    M365AuthoritySurface::ModelProviderTransport;

pub use config::Config;
pub use web::Gateway;
