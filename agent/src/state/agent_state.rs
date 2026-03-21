use wasmtime::{Caller, Config, Engine, Linker, Module, Store};

use crate::pb::{TaskAssignment, TaskResult, TaskStatus, WorkerSignal, WorkerStatus};
use std::sync::{
    Arc, RwLock,
    atomic::{AtomicI16, Ordering},
};

#[derive(Debug, Default)]
pub struct AgentState {
    status: RwLock<WorkerStatus>,
    tasks: RwLock<Vec<TaskAssignment>>,
    task_counter: AtomicI16,
    engine: Engine,
}
struct TaskContext {
    task_id: String,
    limits: wasmtime::StoreLimits,
}

impl AgentState {
    pub fn new(status: WorkerStatus) -> Self {
        let mut engine_config = Config::new();
        engine_config.consume_fuel(true);

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
    pub fn perform_task(
        &self,
        task: TaskAssignment,
        available_memory: u64,
        total_memory: u64,
    ) -> Result<(), String> {
        if available_memory < total_memory / 10 {
            return Err("insufficient memory".to_string());
        }
        self.tasks.write().unwrap().push(task);
        self.task_counter.fetch_add(1, Ordering::Relaxed);
        Ok(())
    }
    pub fn complete_task(&self, task_id: String) {
        self.tasks.write().unwrap().retain(|t| t.task_id != task_id);
        self.task_counter.fetch_sub(1, Ordering::Relaxed);
    }

    pub fn task_failed(&self, task_id: String, error: String) {
        self.tasks.write().unwrap().retain(|t| t.task_id != task_id);
        self.task_counter.fetch_sub(1, Ordering::Relaxed);
        eprintln!("task {} failed: {}", task_id, error);
    }
    pub fn execute_task(
        self: &Arc<Self>,
        workers_total_available_memory: u64,
        result_tx: tokio::sync::mpsc::Sender<WorkerSignal>,
    ) -> wasmtime::Result<(), String> {
        if self.task_counter.load(Ordering::Acquire) < 1 {
            return Ok(());
        }
        let tasks: Vec<TaskAssignment> = self.tasks.read().unwrap().iter().cloned().collect();
        for task in tasks {
            let state = self.clone();
            let tx = result_tx.clone();
            tokio::task::spawn_blocking(move || {
                let res = AgentState::run_wasm(
                    &state.engine,
                    &task.wasm_binary,
                    &task.task_id,
                    &task.fuel_limit,
                    workers_total_available_memory,
                );
                let result = match res {
                    Ok(code) => {
                        state.complete_task(task.task_id.clone());
                        TaskResult {
                            task_id: task.task_id,
                            status: TaskStatus::Success as i32,
                            exit_code: code,
                            completed_at: chrono::Utc::now().timestamp(),
                            ..Default::default()
                        }
                    }
                    Err(e) => {
                        state.task_failed(task.task_id.clone(), e.to_string());
                        TaskResult {
                            task_id: task.task_id,
                            status: TaskStatus::Failed as i32,
                            error: e.to_string(),
                            completed_at: chrono::Utc::now().timestamp(),
                            ..Default::default()
                        }
                    }
                };
                tokio::runtime::Handle::current().block_on(async {
                    let _ = tx
                        .send(WorkerSignal {
                            status: state.get_status() as i32,
                            result: Some(result),
                            ..Default::default()
                        })
                        .await;
                })
            });
        }
        Ok(())
    }
    pub fn run_wasm(
        engine: &Engine,
        wasm_binary: &[u8],
        task_id: &str,
        fuel: &u64,
        worker_total_available_memory: u64,
    ) -> wasmtime::Result<i32> {
        let module = Module::new(engine, wasm_binary)?;
        let ceiling = (worker_total_available_memory as f64 * 0.8) as u64;
        let memory_limit = module
            .exports()
            .filter_map(|e| {
                if let wasmtime::ExternType::Memory(mem) = e.ty() {
                    mem.maximum().map(|pages| pages * 64 * 1024)
                } else {
                    None
                }
            })
            .next()
            .map(|declared| declared.min(ceiling))
            .unwrap_or(ceiling / 8);
        let mut linker: Linker<TaskContext> = Linker::new(engine);

        linker.func_wrap(
            "host",
            "log",
            |caller: Caller<'_, TaskContext>, _ptr: i32, _len: i32| {
                println!("[WASM LOG] [{}] is executing", caller.data().task_id);
            },
        )?;

        let mut store = Store::new(
            engine,
            TaskContext {
                task_id: task_id.to_string(),
                limits: wasmtime::StoreLimitsBuilder::new()
                    .memory_size(memory_limit as usize)
                    .memories(1)
                    .build(),
            },
        );
        store.set_fuel(*fuel)?;
        store.limiter(|ctx| &mut ctx.limits);

        let instance = linker.instantiate(&mut store, &module)?;
        let run = instance.get_typed_func::<(), i32>(&mut store, "run")?;
        let result = run.call(&mut store, ())?;

        Ok(result)
    }
}
