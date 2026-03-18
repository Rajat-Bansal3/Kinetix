#[derive(Debug, Default)]
pub struct AgentState {
    orchestrator_url: &'static str,
}

impl AgentState {
    pub fn new(target_url: &'static str) -> Self {
        Self {
            orchestrator_url: target_url,
        }
    }
}
