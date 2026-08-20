use std::{
    collections::{HashMap, VecDeque},
    sync::{Arc, Mutex},
    time::{Duration, SystemTime},
};

use axum::http::StatusCode;
use serde::Serialize;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::{sync::Notify, time::Instant};

const INTERACTIVE_QUEUE_MAX_WAITING: usize = 64;
const MEMORY_QUEUE_MAX_WAITING: usize = 8;
const SHARED_MAX_CONCURRENT: usize = 2;
const MILESTONE_MEMORY_LEASE: Duration = Duration::from_secs(300);
const DEFAULT_RECOVERY_OBSERVATION: Duration = Duration::from_secs(60);
const DEFAULT_COOLDOWNS: [Duration; 5] = [
    Duration::from_secs(1_125),
    Duration::from_secs(2_250),
    Duration::from_secs(4_500),
    Duration::from_secs(9_000),
    Duration::from_secs(18_000),
];

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum WorkloadClass {
    ExternalUser,
    Autonomous,
    AsyncCompletion,
    Memory,
}

impl WorkloadClass {
    fn is_interactive(self) -> bool {
        self != Self::Memory
    }

    fn is_external(self) -> bool {
        self == Self::ExternalUser
    }

    fn is_background(self) -> bool {
        matches!(self, Self::Autonomous | Self::AsyncCompletion)
    }
}

#[derive(Clone, Copy, Debug)]
pub struct TrafficLimits {
    pub interactive_queue_timeout: Duration,
    pub memory_queue_timeout: Duration,
}

impl Default for TrafficLimits {
    fn default() -> Self {
        Self {
            interactive_queue_timeout: Duration::from_secs(120),
            memory_queue_timeout: Duration::from_secs(120),
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CircuitState {
    Closed,
    Open,
    HalfOpenReady,
    ProbeInFlight,
    Recovery,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AdmissionError {
    pub status: StatusCode,
    pub code: &'static str,
    pub retry_after_seconds: u64,
    pub message: &'static str,
}

impl std::fmt::Display for AdmissionError {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(self.message)
    }
}

impl std::error::Error for AdmissionError {}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TrafficSnapshot {
    pub interactive_in_flight: usize,
    pub interactive_waiting: usize,
    pub external_user_in_flight: usize,
    pub autonomous_in_flight: usize,
    pub autonomous_waiting: usize,
    pub memory_in_flight: usize,
    pub memory_waiting: usize,
    pub memory_pending_count: usize,
    pub oldest_memory_age_seconds: u64,
    pub memory_yield_pending: bool,
    pub memory_yield_active: bool,
    pub last_memory_yield_outcome: String,
    pub last_memory_yield_duration_ms: u64,
    pub memory_429_count: u64,
    pub shared_429_count: u64,
    pub last_429_source: String,
    pub shared_circuit_state: CircuitState,
    pub shared_cooldown_level: usize,
    pub shared_cooldown_remaining_seconds: u64,
    pub recovery_observation_seconds: u64,
    pub recovery_observation_remaining_seconds: u64,
    pub last_recovery_mode: String,
    pub last_recovery_reason: String,
    pub last_recovery_at: String,
}

pub struct TrafficController {
    state: Mutex<State>,
    changed: Notify,
    cooldowns: [Duration; 5],
    recovery_observation: Duration,
}

struct State {
    next_id: u64,
    interactive_queue: VecDeque<Waiter>,
    memory_queue: VecDeque<MemoryWaiter>,
    interactive_in_flight: usize,
    external_in_flight: usize,
    autonomous_in_flight: usize,
    memory_in_flight: usize,
    circuit: CircuitState,
    cooldown_level: usize,
    cooldown_until: Option<Instant>,
    recovery_quiet_since: Option<Instant>,
    last_recovery_mode: String,
    last_recovery_reason: String,
    last_recovery_at: String,
    memory_429_count: u64,
    shared_429_count: u64,
    last_429_source: String,
    memory_yield: Option<MemoryYield>,
    last_memory_yield_outcome: String,
    last_memory_yield_duration: Duration,
    seen_hindsight_events: HashMap<String, SystemTime>,
}

#[derive(Clone, Copy)]
struct Waiter {
    id: u64,
    class: WorkloadClass,
}

#[derive(Clone, Copy)]
struct MemoryWaiter {
    id: u64,
    queued_at: Instant,
}

struct MemoryYield {
    armed_at: Instant,
    started_at: Option<Instant>,
    deadline: Instant,
    active: bool,
}

pub struct Permit {
    controller: Arc<TrafficController>,
    class: WorkloadClass,
    finished: bool,
}

impl TrafficController {
    pub fn new() -> Arc<Self> {
        Self::with_policy(DEFAULT_COOLDOWNS, DEFAULT_RECOVERY_OBSERVATION)
    }

    fn with_policy(cooldowns: [Duration; 5], recovery_observation: Duration) -> Arc<Self> {
        Arc::new(Self {
            state: Mutex::new(State {
                next_id: 0,
                interactive_queue: VecDeque::new(),
                memory_queue: VecDeque::new(),
                interactive_in_flight: 0,
                external_in_flight: 0,
                autonomous_in_flight: 0,
                memory_in_flight: 0,
                circuit: CircuitState::Closed,
                cooldown_level: 0,
                cooldown_until: None,
                recovery_quiet_since: None,
                last_recovery_mode: String::new(),
                last_recovery_reason: String::new(),
                last_recovery_at: String::new(),
                memory_429_count: 0,
                shared_429_count: 0,
                last_429_source: String::new(),
                memory_yield: None,
                last_memory_yield_outcome: String::new(),
                last_memory_yield_duration: Duration::ZERO,
                seen_hindsight_events: HashMap::new(),
            }),
            changed: Notify::new(),
            cooldowns,
            recovery_observation,
        })
    }

    pub async fn acquire(
        self: &Arc<Self>,
        class: WorkloadClass,
        limits: TrafficLimits,
    ) -> Result<Permit, AdmissionError> {
        if class == WorkloadClass::Memory {
            self.acquire_memory(limits).await
        } else {
            self.acquire_interactive(class, limits).await
        }
    }

    async fn acquire_interactive(
        self: &Arc<Self>,
        class: WorkloadClass,
        limits: TrafficLimits,
    ) -> Result<Permit, AdmissionError> {
        debug_assert!(class.is_interactive());
        let ordinary_deadline = Instant::now() + limits.interactive_queue_timeout;
        let id = {
            let mut state = self.state.lock().expect("traffic state poisoned");
            state.refresh(Instant::now(), self.recovery_observation);
            if state.interactive_queue.len() >= INTERACTIVE_QUEUE_MAX_WAITING {
                return Err(capacity_error(
                    "interactive_capacity_busy",
                    limits.interactive_queue_timeout,
                    state.next_id,
                    "interactive waiting queue is full",
                ));
            }
            let id = state.next_id;
            state.next_id += 1;
            let waiter = Waiter { id, class };
            if class.is_external() {
                let position = state
                    .interactive_queue
                    .iter()
                    .position(|queued| !queued.class.is_external())
                    .unwrap_or(state.interactive_queue.len());
                state.interactive_queue.insert(position, waiter);
            } else {
                state.interactive_queue.push_back(waiter);
            }
            id
        };

        loop {
            let notified = self.changed.notified();
            let deadline = {
                let mut state = self.state.lock().expect("traffic state poisoned");
                let now = Instant::now();
                state.refresh(now, self.recovery_observation);
                if class.is_external() && state.memory_yield.is_some() {
                    state.finish_memory_yield("preempted_by_interactive", now);
                }
                if state.can_admit_interactive(id, class, now) {
                    state.remove_interactive(id);
                    state.interactive_in_flight += 1;
                    if class.is_external() {
                        state.external_in_flight += 1;
                    } else {
                        state.autonomous_in_flight += 1;
                    }
                    if state.circuit == CircuitState::HalfOpenReady {
                        state.circuit = CircuitState::ProbeInFlight;
                    }
                    return Ok(Permit {
                        controller: Arc::clone(self),
                        class,
                        finished: false,
                    });
                }
                if now >= ordinary_deadline
                    && !(class == WorkloadClass::Autonomous
                        && state
                            .memory_yield
                            .as_ref()
                            .is_some_and(|yield_state| now < yield_state.deadline))
                {
                    state.remove_interactive(id);
                    return Err(capacity_error(
                        "interactive_capacity_busy",
                        limits.interactive_queue_timeout,
                        id,
                        "interactive admission timed out",
                    ));
                }
                state
                    .memory_yield
                    .as_ref()
                    .filter(|_| class == WorkloadClass::Autonomous)
                    .map(|yield_state| ordinary_deadline.max(yield_state.deadline))
                    .unwrap_or(ordinary_deadline)
            };

            if tokio::time::timeout_at(deadline, notified).await.is_err() {
                self.changed.notify_waiters();
            }
        }
    }

    async fn acquire_memory(
        self: &Arc<Self>,
        limits: TrafficLimits,
    ) -> Result<Permit, AdmissionError> {
        let deadline = Instant::now() + limits.memory_queue_timeout;
        let id = {
            let mut state = self.state.lock().expect("traffic state poisoned");
            let now = Instant::now();
            state.refresh(now, self.recovery_observation);
            if state.circuit != CircuitState::Closed {
                return Err(state.throttle_error(now, &self.cooldowns));
            }
            if state.memory_queue.len() >= MEMORY_QUEUE_MAX_WAITING {
                return Err(capacity_error(
                    "memory_capacity_deferred",
                    limits.memory_queue_timeout,
                    state.next_id,
                    "memory waiting queue is full",
                ));
            }
            let id = state.next_id;
            state.next_id += 1;
            state
                .memory_queue
                .push_back(MemoryWaiter { id, queued_at: now });
            id
        };

        loop {
            let notified = self.changed.notified();
            {
                let mut state = self.state.lock().expect("traffic state poisoned");
                let now = Instant::now();
                state.refresh(now, self.recovery_observation);
                if state.circuit != CircuitState::Closed {
                    state.remove_memory(id);
                    return Err(state.throttle_error(now, &self.cooldowns));
                }
                if state.can_admit_memory(id) {
                    state.remove_memory(id);
                    state.memory_in_flight = 1;
                    if let Some(yield_state) = state.memory_yield.as_mut() {
                        yield_state.active = true;
                        yield_state.started_at = Some(now);
                    }
                    return Ok(Permit {
                        controller: Arc::clone(self),
                        class: WorkloadClass::Memory,
                        finished: false,
                    });
                }
                if now >= deadline {
                    state.remove_memory(id);
                    return Err(capacity_error(
                        "interactive_capacity_busy",
                        limits.memory_queue_timeout,
                        id,
                        "memory admission timed out",
                    ));
                }
            }
            if tokio::time::timeout_at(deadline, notified).await.is_err() {
                self.changed.notify_waiters();
            }
        }
    }

    pub fn arm_memory_yield(&self) {
        let mut state = self.state.lock().expect("traffic state poisoned");
        let now = Instant::now();
        state.memory_yield = Some(MemoryYield {
            armed_at: now,
            started_at: None,
            deadline: now + MILESTONE_MEMORY_LEASE,
            active: false,
        });
        state.last_memory_yield_outcome = "pending".to_owned();
        state.last_memory_yield_duration = Duration::ZERO;
        drop(state);
        self.changed.notify_waiters();
    }

    pub fn observe_hindsight_event(
        &self,
        event: &str,
        operation_id: &str,
        completed: bool,
        event_at: SystemTime,
    ) {
        if !completed {
            return;
        }
        let mut state = self.state.lock().expect("traffic state poisoned");
        let key = format!("{event}\0{operation_id}");
        if !operation_id.is_empty() && state.seen_hindsight_events.contains_key(&key) {
            return;
        }
        if !operation_id.is_empty() {
            if state.seen_hindsight_events.len() >= 256
                && let Some(oldest) = state
                    .seen_hindsight_events
                    .iter()
                    .min_by_key(|(_, seen_at)| **seen_at)
                    .map(|(key, _)| key.clone())
            {
                state.seen_hindsight_events.remove(&oldest);
            }
            state.seen_hindsight_events.insert(key, event_at);
        }
        if event == "retain.completed"
            && state
                .memory_yield
                .as_ref()
                .is_some_and(|yield_state| Instant::now() >= yield_state.armed_at)
        {
            state.finish_memory_yield("retain_durable", Instant::now());
        }
        drop(state);
        self.changed.notify_waiters();
    }

    pub fn complete_recovery(&self) -> Result<(), &'static str> {
        let mut state = self.state.lock().expect("traffic state poisoned");
        state.refresh(Instant::now(), self.recovery_observation);
        if state.circuit != CircuitState::Recovery {
            return Err("shared circuit is not RECOVERY");
        }
        state.close_recovery("manual", "operator_complete");
        drop(state);
        self.changed.notify_waiters();
        Ok(())
    }

    pub fn snapshot(&self) -> TrafficSnapshot {
        let mut state = self.state.lock().expect("traffic state poisoned");
        let now = Instant::now();
        state.refresh(now, self.recovery_observation);
        let autonomous_waiting = state
            .interactive_queue
            .iter()
            .filter(|waiter| waiter.class.is_background())
            .count();
        let (memory_yield_pending, memory_yield_active) = state
            .memory_yield
            .as_ref()
            .map(|yield_state| (!yield_state.active, yield_state.active))
            .unwrap_or_default();
        TrafficSnapshot {
            interactive_in_flight: state.interactive_in_flight,
            interactive_waiting: state.interactive_queue.len(),
            external_user_in_flight: state.external_in_flight,
            autonomous_in_flight: state.autonomous_in_flight,
            autonomous_waiting,
            memory_in_flight: state.memory_in_flight,
            memory_waiting: state.memory_queue.len(),
            memory_pending_count: state.memory_in_flight + state.memory_queue.len(),
            oldest_memory_age_seconds: state
                .memory_queue
                .front()
                .map(|waiter| now.saturating_duration_since(waiter.queued_at).as_secs())
                .unwrap_or_default(),
            memory_yield_pending,
            memory_yield_active,
            last_memory_yield_outcome: state.last_memory_yield_outcome.clone(),
            last_memory_yield_duration_ms: state.last_memory_yield_duration.as_millis() as u64,
            memory_429_count: state.memory_429_count,
            shared_429_count: state.shared_429_count,
            last_429_source: state.last_429_source.clone(),
            shared_circuit_state: state.circuit,
            shared_cooldown_level: state.cooldown_level,
            shared_cooldown_remaining_seconds: state
                .cooldown_until
                .map(|until| rounded_up_seconds(until.saturating_duration_since(now)))
                .unwrap_or_default(),
            recovery_observation_seconds: self.recovery_observation.as_secs(),
            recovery_observation_remaining_seconds: state
                .recovery_quiet_since
                .filter(|_| state.circuit == CircuitState::Recovery)
                .map(|since| {
                    rounded_up_seconds(
                        self.recovery_observation
                            .saturating_sub(now.saturating_duration_since(since)),
                    )
                })
                .unwrap_or_default(),
            last_recovery_mode: state.last_recovery_mode.clone(),
            last_recovery_reason: state.last_recovery_reason.clone(),
            last_recovery_at: state.last_recovery_at.clone(),
        }
    }

    fn release(&self, class: WorkloadClass, status: StatusCode, retry_after: Option<&str>) {
        let mut state = self.state.lock().expect("traffic state poisoned");
        let now = Instant::now();
        if class == WorkloadClass::Memory {
            state.memory_in_flight = state.memory_in_flight.saturating_sub(1);
            if status == StatusCode::TOO_MANY_REQUESTS {
                state.memory_429_count += 1;
                state.apply_rate_limit("memory", now, retry_after, &self.cooldowns);
            }
        } else {
            state.interactive_in_flight = state.interactive_in_flight.saturating_sub(1);
            if class.is_external() {
                state.external_in_flight = state.external_in_flight.saturating_sub(1);
            } else {
                state.autonomous_in_flight = state.autonomous_in_flight.saturating_sub(1);
            }
            if status == StatusCode::TOO_MANY_REQUESTS {
                state.apply_rate_limit("interactive", now, retry_after, &self.cooldowns);
            } else if state.circuit == CircuitState::ProbeInFlight {
                if status.is_success() && class.is_external() {
                    state.circuit = CircuitState::Recovery;
                    state.recovery_quiet_since = Some(now);
                } else {
                    state.circuit = CircuitState::HalfOpenReady;
                    state.recovery_quiet_since = None;
                }
            } else if state.circuit == CircuitState::Recovery {
                state.recovery_quiet_since = Some(now);
            }
            if class == WorkloadClass::AsyncCompletion && status.is_success() {
                drop(state);
                self.arm_memory_yield();
                self.changed.notify_waiters();
                return;
            }
        }
        drop(state);
        self.changed.notify_waiters();
    }
}

impl State {
    fn shared_in_flight(&self) -> usize {
        self.interactive_in_flight + self.memory_in_flight
    }

    fn refresh(&mut self, now: Instant, recovery_observation: Duration) {
        if self.circuit == CircuitState::Open
            && self.cooldown_until.is_none_or(|until| now >= until)
        {
            self.circuit = CircuitState::HalfOpenReady;
        }
        if self.circuit == CircuitState::Recovery
            && self.shared_in_flight() == 0
            && self.interactive_queue.is_empty()
            && self.memory_queue.is_empty()
            && self
                .recovery_quiet_since
                .is_some_and(|since| now.saturating_duration_since(since) >= recovery_observation)
        {
            self.close_recovery("automatic", "quiet_observation_completed");
        }
        if self
            .memory_yield
            .as_ref()
            .is_some_and(|yield_state| now >= yield_state.deadline)
        {
            self.finish_memory_yield("timeout", now);
        }
    }

    fn can_admit_interactive(&self, id: u64, class: WorkloadClass, now: Instant) -> bool {
        if self.interactive_queue.front().map(|waiter| waiter.id) != Some(id) {
            return false;
        }
        if class.is_background() && self.autonomous_in_flight >= 1 {
            return false;
        }
        if class == WorkloadClass::Autonomous && self.memory_yield.is_some() {
            return false;
        }
        if !class.is_external() && self.memory_head_can_use_slot() {
            return false;
        }
        match self.circuit {
            CircuitState::Closed => {
                self.shared_in_flight() < SHARED_MAX_CONCURRENT
                    && self.cooldown_until.is_none_or(|until| now >= until)
            }
            CircuitState::HalfOpenReady => class.is_external() && self.shared_in_flight() == 0,
            CircuitState::Recovery => self.shared_in_flight() < 1,
            CircuitState::Open | CircuitState::ProbeInFlight => false,
        }
    }

    fn can_admit_memory(&self, id: u64) -> bool {
        self.memory_queue.front().map(|waiter| waiter.id) == Some(id)
            && self.memory_head_can_use_slot()
    }

    fn memory_head_can_use_slot(&self) -> bool {
        !self.memory_queue.is_empty()
            && self.memory_in_flight == 0
            && !self
                .interactive_queue
                .iter()
                .any(|waiter| waiter.class.is_external())
            && self.shared_in_flight() < SHARED_MAX_CONCURRENT
            && self.circuit == CircuitState::Closed
    }

    fn remove_interactive(&mut self, id: u64) {
        if let Some(position) = self
            .interactive_queue
            .iter()
            .position(|waiter| waiter.id == id)
        {
            self.interactive_queue.remove(position);
        }
    }

    fn remove_memory(&mut self, id: u64) {
        if let Some(position) = self.memory_queue.iter().position(|waiter| waiter.id == id) {
            self.memory_queue.remove(position);
        }
    }

    fn finish_memory_yield(&mut self, outcome: &str, now: Instant) {
        let Some(yield_state) = self.memory_yield.take() else {
            return;
        };
        let started_at = yield_state.started_at.unwrap_or(yield_state.armed_at);
        self.last_memory_yield_duration = now.saturating_duration_since(started_at);
        self.last_memory_yield_outcome = outcome.to_owned();
    }

    fn close_recovery(&mut self, mode: &str, reason: &str) {
        self.circuit = CircuitState::Closed;
        self.cooldown_level = 0;
        self.cooldown_until = None;
        self.recovery_quiet_since = None;
        self.last_recovery_mode = mode.to_owned();
        self.last_recovery_reason = reason.to_owned();
        self.last_recovery_at = OffsetDateTime::now_utc()
            .format(&Rfc3339)
            .expect("RFC 3339 formatting is infallible");
    }

    fn apply_rate_limit(
        &mut self,
        source: &str,
        now: Instant,
        retry_after: Option<&str>,
        cooldowns: &[Duration; 5],
    ) {
        self.cooldown_level = (self.cooldown_level + 1).min(cooldowns.len());
        let mut until = now + cooldowns[self.cooldown_level - 1];
        if let Some(delay) = retry_after.and_then(parse_retry_after)
            && now + delay > until
        {
            until = now + delay;
        }
        self.cooldown_until = Some(until);
        self.circuit = CircuitState::Open;
        self.recovery_quiet_since = None;
        self.shared_429_count += 1;
        self.last_429_source = source.to_owned();
    }

    fn throttle_error(&self, now: Instant, cooldowns: &[Duration; 5]) -> AdmissionError {
        let level = self.cooldown_level.clamp(1, cooldowns.len());
        let retry_after_seconds = self
            .cooldown_until
            .map(|until| {
                until
                    .saturating_duration_since(now)
                    .as_secs()
                    .saturating_add(1)
            })
            .filter(|seconds| *seconds > 0)
            .unwrap_or(cooldowns[level - 1].as_secs());
        AdmissionError {
            status: StatusCode::TOO_MANY_REQUESTS,
            code: "upstream_throttle",
            retry_after_seconds,
            message: "shared Microsoft account is throttled",
        }
    }
}

impl Permit {
    pub fn finish(mut self, status: StatusCode, retry_after: Option<&str>) {
        self.controller.release(self.class, status, retry_after);
        self.finished = true;
    }
}

impl Drop for Permit {
    fn drop(&mut self) {
        if !self.finished {
            self.controller
                .release(self.class, StatusCode::INTERNAL_SERVER_ERROR, None);
        }
    }
}

fn capacity_error(
    code: &'static str,
    timeout: Duration,
    id: u64,
    message: &'static str,
) -> AdmissionError {
    AdmissionError {
        status: StatusCode::SERVICE_UNAVAILABLE,
        code,
        retry_after_seconds: (2 + id % 4).min(timeout.as_secs().max(1)),
        message,
    }
}

fn parse_retry_after(value: &str) -> Option<Duration> {
    let value = value.trim();
    if let Ok(seconds) = value.parse::<u64>() {
        return Some(Duration::from_secs(seconds));
    }
    let when = httpdate::parse_http_date(value).ok()?;
    when.duration_since(SystemTime::now()).ok()
}

fn rounded_up_seconds(duration: Duration) -> u64 {
    if duration.is_zero() {
        0
    } else {
        duration.as_secs().saturating_add(1)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fast_controller() -> Arc<TrafficController> {
        TrafficController::with_policy(
            [
                Duration::from_millis(20),
                Duration::from_millis(40),
                Duration::from_millis(80),
                Duration::from_millis(160),
                Duration::from_millis(320),
            ],
            Duration::from_millis(60),
        )
    }

    fn fast_limits() -> TrafficLimits {
        TrafficLimits {
            interactive_queue_timeout: Duration::from_millis(80),
            memory_queue_timeout: Duration::from_millis(80),
        }
    }

    async fn enter_recovery(controller: &Arc<TrafficController>) {
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::TOO_MANY_REQUESTS, None);
        tokio::time::sleep(Duration::from_millis(25)).await;
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::OK, None);
        assert_eq!(
            controller.snapshot().shared_circuit_state,
            CircuitState::Recovery
        );
    }

    #[tokio::test]
    async fn memory_and_autonomous_share_two_slots_but_each_class_stays_at_one() {
        let controller = fast_controller();
        let memory = controller
            .acquire(WorkloadClass::Memory, fast_limits())
            .await
            .unwrap();
        let autonomous = controller
            .acquire(WorkloadClass::Autonomous, fast_limits())
            .await
            .unwrap();
        let error = controller
            .acquire(WorkloadClass::Autonomous, fast_limits())
            .await
            .err()
            .unwrap();
        assert_eq!(error.status, StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(controller.snapshot().memory_in_flight, 1);
        assert_eq!(controller.snapshot().autonomous_in_flight, 1);
        memory.finish(StatusCode::OK, None);
        autonomous.finish(StatusCode::OK, None);
    }

    #[tokio::test]
    async fn external_user_moves_ahead_of_queued_background_work() {
        let controller = fast_controller();
        let memory = controller
            .acquire(WorkloadClass::Memory, fast_limits())
            .await
            .unwrap();
        let autonomous = controller
            .acquire(WorkloadClass::Autonomous, fast_limits())
            .await
            .unwrap();

        let queued_background = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::Autonomous, fast_limits())
                    .await
            })
        };
        tokio::task::yield_now().await;
        let external = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::ExternalUser, fast_limits())
                    .await
            })
        };
        tokio::task::yield_now().await;
        autonomous.finish(StatusCode::OK, None);
        let external = external.await.unwrap().unwrap();
        assert_eq!(controller.snapshot().external_user_in_flight, 1);
        external.finish(StatusCode::OK, None);
        memory.finish(StatusCode::OK, None);
        queued_background
            .await
            .unwrap()
            .unwrap()
            .finish(StatusCode::OK, None);
    }

    #[tokio::test]
    async fn p0_then_eligible_memory_then_p2_controls_shared_admission() {
        let controller = fast_controller();
        let held_one = controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap();
        let held_two = controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap();

        let p2 = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::Autonomous, fast_limits())
                    .await
            })
        };
        tokio::task::yield_now().await;
        let memory = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::Memory, fast_limits())
                    .await
            })
        };
        tokio::task::yield_now().await;
        let p0 = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::ExternalUser, fast_limits())
                    .await
            })
        };
        tokio::task::yield_now().await;

        held_one.finish(StatusCode::OK, None);
        let p0 = tokio::time::timeout(Duration::from_millis(20), p0)
            .await
            .unwrap()
            .unwrap()
            .unwrap();
        assert!(!memory.is_finished());
        assert!(!p2.is_finished());

        held_two.finish(StatusCode::OK, None);
        let memory = tokio::time::timeout(Duration::from_millis(20), memory)
            .await
            .unwrap()
            .unwrap()
            .unwrap();
        assert!(!p2.is_finished());

        p0.finish(StatusCode::OK, None);
        let p2 = tokio::time::timeout(Duration::from_millis(20), p2)
            .await
            .unwrap()
            .unwrap()
            .unwrap();
        memory.finish(StatusCode::OK, None);
        p2.finish(StatusCode::OK, None);
    }

    #[tokio::test]
    async fn memory_waiting_buffer_is_eight_and_fifo() {
        let controller = fast_controller();
        let active = controller
            .acquire(WorkloadClass::Memory, fast_limits())
            .await
            .unwrap();
        let limits = TrafficLimits {
            interactive_queue_timeout: Duration::from_secs(1),
            memory_queue_timeout: Duration::from_secs(1),
        };
        let mut waiting = Vec::new();
        for _ in 0..MEMORY_QUEUE_MAX_WAITING {
            let controller = Arc::clone(&controller);
            waiting.push(tokio::spawn(async move {
                controller.acquire(WorkloadClass::Memory, limits).await
            }));
            tokio::task::yield_now().await;
        }
        let ids = controller
            .state
            .lock()
            .unwrap()
            .memory_queue
            .iter()
            .map(|waiter| waiter.id)
            .collect::<Vec<_>>();
        assert_eq!(ids.len(), MEMORY_QUEUE_MAX_WAITING);
        assert!(ids.windows(2).all(|pair| pair[0] < pair[1]));

        let overflow = controller
            .acquire(WorkloadClass::Memory, limits)
            .await
            .err()
            .unwrap();
        assert_eq!(overflow.code, "memory_capacity_deferred");

        active.finish(StatusCode::OK, None);
        for waiter in waiting {
            waiter.await.unwrap().unwrap().finish(StatusCode::OK, None);
        }
    }

    #[test]
    fn ordinary_queue_and_milestone_deadlines_are_distinct() {
        let limits = TrafficLimits::default();
        assert_eq!(limits.interactive_queue_timeout.as_secs(), 120);
        assert_eq!(limits.memory_queue_timeout.as_secs(), 120);
        assert_eq!(MILESTONE_MEMORY_LEASE.as_secs(), 300);
    }

    #[tokio::test]
    async fn autonomous_wait_follows_only_a_live_memory_yield_deadline() {
        let controller = fast_controller();
        controller.arm_memory_yield();
        {
            let mut state = controller.state.lock().unwrap();
            state.memory_yield.as_mut().unwrap().deadline =
                Instant::now() + Duration::from_millis(100);
        }
        let limits = TrafficLimits {
            interactive_queue_timeout: Duration::from_millis(20),
            memory_queue_timeout: Duration::from_millis(20),
        };
        let waiting = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move { controller.acquire(WorkloadClass::Autonomous, limits).await })
        };
        tokio::time::sleep(Duration::from_millis(30)).await;
        assert!(!waiting.is_finished());

        controller.observe_hindsight_event(
            "retain.completed",
            "operation-deadline",
            true,
            SystemTime::now(),
        );
        waiting.await.unwrap().unwrap().finish(StatusCode::OK, None);

        controller.arm_memory_yield();
        let ordinary = controller
            .acquire(WorkloadClass::AsyncCompletion, limits)
            .await
            .unwrap();
        ordinary.finish(StatusCode::OK, None);
    }

    #[tokio::test]
    async fn only_external_user_can_probe_and_success_enters_recovery() {
        let controller = fast_controller();
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::TOO_MANY_REQUESTS, None);
        let memory_error = controller
            .acquire(WorkloadClass::Memory, fast_limits())
            .await
            .err()
            .unwrap();
        assert_eq!(memory_error.status, StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(memory_error.code, "upstream_throttle");

        tokio::time::sleep(Duration::from_millis(25)).await;
        let half_open_memory_error = controller
            .acquire(WorkloadClass::Memory, fast_limits())
            .await
            .err()
            .unwrap();
        assert_eq!(half_open_memory_error.status, StatusCode::TOO_MANY_REQUESTS);
        let background_error = controller
            .acquire(WorkloadClass::Autonomous, fast_limits())
            .await
            .err()
            .unwrap();
        assert_eq!(background_error.status, StatusCode::SERVICE_UNAVAILABLE);
        let async_completion_error = controller
            .acquire(WorkloadClass::AsyncCompletion, fast_limits())
            .await
            .err()
            .unwrap();
        assert_eq!(
            async_completion_error.status,
            StatusCode::SERVICE_UNAVAILABLE
        );
        assert_eq!(
            controller.snapshot().shared_circuit_state,
            CircuitState::HalfOpenReady
        );
        let probe = controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap();
        probe.finish(StatusCode::OK, None);
        assert_eq!(
            controller.snapshot().shared_circuit_state,
            CircuitState::Recovery
        );
        controller.complete_recovery().unwrap();
        let snapshot = controller.snapshot();
        assert_eq!(snapshot.shared_circuit_state, CircuitState::Closed);
        assert_eq!(snapshot.last_recovery_mode, "manual");
        assert_eq!(snapshot.last_recovery_reason, "operator_complete");
        assert!(!snapshot.last_recovery_at.is_empty());
    }

    #[tokio::test]
    async fn cooldown_expiry_only_makes_the_external_probe_ready() {
        let controller = fast_controller();
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::TOO_MANY_REQUESTS, None);

        tokio::time::sleep(Duration::from_millis(50)).await;

        let snapshot = controller.snapshot();
        assert_eq!(snapshot.shared_circuit_state, CircuitState::HalfOpenReady);
        assert_eq!(snapshot.shared_cooldown_level, 1);
        assert_eq!(snapshot.shared_cooldown_remaining_seconds, 0);
    }

    #[tokio::test]
    async fn unsuccessful_probe_never_qualifies_for_auto_recovery() {
        let controller = fast_controller();
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::TOO_MANY_REQUESTS, None);
        tokio::time::sleep(Duration::from_millis(25)).await;
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::BAD_GATEWAY, None);

        tokio::time::sleep(Duration::from_millis(25)).await;

        let snapshot = controller.snapshot();
        assert_eq!(snapshot.shared_circuit_state, CircuitState::HalfOpenReady);
        assert!(snapshot.last_recovery_mode.is_empty());
    }

    #[tokio::test]
    async fn successful_probe_auto_closes_after_a_quiet_recovery_observation() {
        let controller = fast_controller();
        enter_recovery(&controller).await;
        tokio::time::sleep(Duration::from_millis(65)).await;

        let snapshot = controller.snapshot();
        assert_eq!(snapshot.shared_circuit_state, CircuitState::Closed);
        assert_eq!(snapshot.shared_cooldown_level, 0);
        assert_eq!(snapshot.shared_cooldown_remaining_seconds, 0);
        let telemetry = serde_json::to_value(snapshot).unwrap();
        assert_eq!(telemetry["lastRecoveryMode"], "automatic");
        assert_eq!(
            telemetry["lastRecoveryReason"],
            "quiet_observation_completed"
        );
        assert!(
            telemetry["lastRecoveryAt"]
                .as_str()
                .is_some_and(|value| !value.is_empty())
        );
    }

    #[tokio::test]
    async fn recovery_throttle_reopens_and_cancels_auto_close() {
        let controller = fast_controller();
        enter_recovery(&controller).await;
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::TOO_MANY_REQUESTS, None);

        let reopened = controller.snapshot();
        assert_eq!(reopened.shared_circuit_state, CircuitState::Open);
        assert_eq!(reopened.shared_cooldown_level, 2);
        tokio::time::sleep(Duration::from_millis(50)).await;

        let after_cooldown = controller.snapshot();
        assert_eq!(
            after_cooldown.shared_circuit_state,
            CircuitState::HalfOpenReady
        );
        assert_eq!(after_cooldown.shared_cooldown_level, 2);
        assert!(after_cooldown.last_recovery_mode.is_empty());
    }

    #[test]
    fn production_cooldown_ladder_is_unchanged() {
        assert_eq!(
            DEFAULT_COOLDOWNS.map(|duration| duration.as_secs()),
            [1_125, 2_250, 4_500, 9_000, 18_000]
        );
        assert_eq!(DEFAULT_RECOVERY_OBSERVATION.as_secs(), 60);
    }

    #[tokio::test]
    async fn recovery_observation_is_independent_from_l1_cooldown() {
        let controller = fast_controller();
        enter_recovery(&controller).await;

        tokio::time::sleep(Duration::from_millis(25)).await;
        assert_eq!(
            controller.snapshot().shared_circuit_state,
            CircuitState::Recovery
        );

        tokio::time::sleep(Duration::from_millis(40)).await;
        assert_eq!(
            controller.snapshot().shared_circuit_state,
            CircuitState::Closed
        );
    }

    #[tokio::test]
    async fn recovery_drains_one_shared_request_at_a_time_before_auto_close() {
        let controller = fast_controller();
        enter_recovery(&controller).await;
        let first = controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap();
        let second = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::ExternalUser, fast_limits())
                    .await
            })
        };
        tokio::task::yield_now().await;
        tokio::time::sleep(Duration::from_millis(65)).await;

        let held = controller.snapshot();
        assert_eq!(held.shared_circuit_state, CircuitState::Recovery);
        assert_eq!(held.interactive_in_flight, 1);
        assert_eq!(held.interactive_waiting, 1);

        first.finish(StatusCode::OK, None);
        let second = second.await.unwrap().unwrap();
        let draining = controller.snapshot();
        assert_eq!(draining.shared_circuit_state, CircuitState::Recovery);
        assert_eq!(draining.interactive_in_flight, 1);
        second.finish(StatusCode::OK, None);
        tokio::time::sleep(Duration::from_millis(65)).await;
        assert_eq!(
            controller.snapshot().shared_circuit_state,
            CircuitState::Closed
        );

        let autonomous = controller
            .acquire(WorkloadClass::Autonomous, fast_limits())
            .await
            .unwrap();
        let external = controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap();
        let third = controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .err()
            .unwrap();
        assert_eq!(third.status, StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(controller.snapshot().interactive_in_flight, 2);
        autonomous.finish(StatusCode::OK, None);
        external.finish(StatusCode::OK, None);
    }

    #[tokio::test]
    async fn retain_durable_releases_autonomous_memory_yield() {
        let controller = fast_controller();
        controller.arm_memory_yield();
        let waiting = {
            let controller = Arc::clone(&controller);
            tokio::spawn(async move {
                controller
                    .acquire(WorkloadClass::Autonomous, fast_limits())
                    .await
            })
        };
        tokio::time::sleep(Duration::from_millis(10)).await;
        assert!(controller.snapshot().memory_yield_pending);
        controller.observe_hindsight_event(
            "retain.completed",
            "operation-1",
            true,
            SystemTime::now(),
        );
        waiting.await.unwrap().unwrap().finish(StatusCode::OK, None);
        assert_eq!(
            controller.snapshot().last_memory_yield_outcome,
            "retain_durable"
        );
    }

    #[tokio::test]
    async fn projected_memory_throttle_does_not_amplify_breaker_state() {
        let controller = fast_controller();
        controller
            .acquire(WorkloadClass::ExternalUser, fast_limits())
            .await
            .unwrap()
            .finish(StatusCode::TOO_MANY_REQUESTS, None);
        let before = controller.snapshot();

        let projected = controller
            .acquire(WorkloadClass::Memory, fast_limits())
            .await
            .err()
            .unwrap();
        let after = controller.snapshot();

        assert_eq!(projected.status, StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(projected.code, "upstream_throttle");
        assert_eq!(after.shared_circuit_state, before.shared_circuit_state);
        assert_eq!(after.shared_cooldown_level, before.shared_cooldown_level);
        assert_eq!(after.shared_429_count, before.shared_429_count);
        assert_eq!(after.memory_429_count, before.memory_429_count);
        assert_eq!(after.last_429_source, before.last_429_source);
    }
}
