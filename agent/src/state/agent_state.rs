use wasmtime::{Config, Engine};

use crate::pb::{TaskAssignment, WorkerStatus};
use std::sync::{
    RwLock,
    atomic::{AtomicI16, Ordering},
};

#[derive(Debug, Default)]
pub struct AgentState {
    status: RwLock<WorkerStatus>,
    tasks: RwLock<Vec<TaskAssignment>>,
    task_counter: AtomicI16,
    engine: Engine,
}

impl AgentState {
    pub fn new(status: WorkerStatus) -> Self {
        let engine_config = Config::new();
        Self {
            status: RwLock::new(status),
            tasks: RwLock::new(Vec::new()),
            task_counter: AtomicI16::new(0),
            engine: Engine::new(&engine_config).unwrap(),
        }
    }

    pub fn get_status(&self) -> WorkerStatus {
        match self.tasks.read().unwrap().len() {
            0 => WorkerStatus::Idle,
            _ => WorkerStatus::Busy,
        }
    }

    pub fn set_status(&self, status: WorkerStatus) {
        *self.status.write().unwrap() = status;
    }
    pub fn perform_task(&self, task: TaskAssignment) {
        self.tasks.write().unwrap().push(task);
        self.task_counter.fetch_add(1, Ordering::Relaxed);
    }
    pub fn execute_task() {}
}
