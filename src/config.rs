use std::{
    env,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    path::PathBuf,
    time::Duration,
};

use crate::error::GatewayError;

const DEFAULT_LISTEN: SocketAddr = SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 4141);

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Config {
    pub listen: SocketAddr,
    pub data_dir: PathBuf,
    pub chat_timeout: Duration,
    pub image_timeout: Duration,
    pub text_input_limit_utf16: usize,
    pub max_tool_calls_per_turn: usize,
    pub max_tool_rounds: usize,
    pub hermes_max_tool_rounds: usize,
}

impl Config {
    pub fn from_env() -> Result<Self, GatewayError> {
        let data_dir = env::var_os("M365_DATA_DIR")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from("data"));
        let listen = env::var("M365_LISTEN")
            .ok()
            .map(|value| parse_socket("M365_LISTEN", &value))
            .transpose()?
            .unwrap_or(DEFAULT_LISTEN);

        Ok(Self {
            listen,
            data_dir,
            chat_timeout: env_seconds("M365_CHAT_TIMEOUT_SECONDS", 1_800)?,
            image_timeout: env_seconds("M365_IMAGE_TIMEOUT_SECONDS", 300)?,
            text_input_limit_utf16: env_usize("M365_TEXT_INPUT_LIMIT_UTF16", 128_000, 1)?,
            max_tool_calls_per_turn: env_usize("M365_MAX_TOOL_CALLS_PER_TURN", 2, 1)?,
            max_tool_rounds: env_usize("M365_MAX_TOOL_ROUNDS", 16, 1)?,
            hermes_max_tool_rounds: env_usize("M365_HERMES_MAX_TOOL_ROUNDS", 128, 1)?,
        })
    }

    pub fn for_test(data_dir: PathBuf) -> Self {
        Self {
            listen: DEFAULT_LISTEN,
            data_dir,
            chat_timeout: Duration::from_secs(1_800),
            image_timeout: Duration::from_secs(300),
            text_input_limit_utf16: 128_000,
            max_tool_calls_per_turn: 2,
            max_tool_rounds: 16,
            hermes_max_tool_rounds: 128,
        }
    }
}

fn parse_socket(name: &str, value: &str) -> Result<SocketAddr, GatewayError> {
    value
        .parse()
        .map_err(|_| GatewayError::Configuration(format!("{name} must be an IP address and port")))
}

fn env_seconds(name: &str, default: u64) -> Result<Duration, GatewayError> {
    let value = match env::var(name) {
        Ok(value) => value,
        Err(env::VarError::NotPresent) => return Ok(Duration::from_secs(default)),
        Err(error) => return Err(GatewayError::Configuration(format!("{name}: {error}"))),
    };
    let seconds = value
        .parse::<u64>()
        .map_err(|_| GatewayError::Configuration(format!("{name} must be a positive integer")))?;
    if seconds == 0 {
        return Err(GatewayError::Configuration(format!(
            "{name} must be a positive integer"
        )));
    }
    Ok(Duration::from_secs(seconds))
}
fn env_usize(name: &str, default: usize, minimum: usize) -> Result<usize, GatewayError> {
    let value = match env::var(name) {
        Ok(value) => value,
        Err(env::VarError::NotPresent) => return Ok(default),
        Err(error) => return Err(GatewayError::Configuration(format!("{name}: {error}"))),
    };
    let value = value.parse::<usize>().map_err(|_| {
        GatewayError::Configuration(format!("{name} must be an integer at least {minimum}"))
    })?;
    if value < minimum {
        return Err(GatewayError::Configuration(format!(
            "{name} must be an integer at least {minimum}"
        )));
    }
    Ok(value)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_config_preserves_current_policy_defaults() {
        let config = Config::for_test(PathBuf::from("test-data"));
        assert_eq!(config.listen, "127.0.0.1:4141".parse().unwrap());
        assert_eq!(config.text_input_limit_utf16, 128_000);
        assert_eq!(config.max_tool_rounds, 16);
        assert_eq!(config.hermes_max_tool_rounds, 128);
    }
}
