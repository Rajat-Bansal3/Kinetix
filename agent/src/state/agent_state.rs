use wasmtime::{Caller, Config, Engine, Linker, Module, Store};

use crate::pb::{TaskAssignment, WorkerStatus};
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
    pub fn perform_task(&self, task: TaskAssignment) {
        self.tasks.write().unwrap().push(task);
        self.task_counter.fetch_add(1, Ordering::Relaxed);
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
    pub fn execute_task(self: &Arc<Self>) -> wasmtime::Result<(), String> {
        if self.task_counter.load(Ordering::Acquire) < 1 {
            return Ok(());
        }
        let tasks: Vec<TaskAssignment> = self.tasks.read().unwrap().iter().cloned().collect();
        println!("executing task");
        for task in tasks {
            println!("executing task , {:?}", task);

            let state = self.clone();
            tokio::task::spawn_blocking(move || {
                let res = AgentState::run_wasm(
                    &state.engine,
                    &task.wasm_binary,
                    &task.task_id,
                    &task.fuel_limit,
                );
                match res {
                    Ok(code) => {
                        println!("[{}] exited with code {}", task.task_id, code);
                        state.complete_task(task.task_id);
                    }
                    Err(e) => {
                        eprintln!("[{}] failed: {}", task.task_id, e);
                        state.task_failed(task.task_id, e.to_string());
                    }
                }
            });
        }
        Ok(())
    }
    pub fn run_wasm(
        engine: &Engine,
        wasm_binary: &[u8],
        task_id: &str,
        fuel: &u64,
    ) -> wasmtime::Result<i32> {
        let module = Module::new(engine, wasm_binary)?;
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
                    .memory_size(64 * 1024 * 1024)
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
