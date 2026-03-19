mod state;
use std::sync::Arc;

use state::agent_state::AgentState;
pub mod pb {
    tonic::include_proto!("kinetix");
}
use crate::pb::{WorkerSignal, WorkerStatus, orchestrator_client::OrchestratorClient};
use sysinfo;
use tokio_stream::wrappers::ReceiverStream;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let grpc_url = "http://127.0.0.1:50051";
    let mut client = OrchestratorClient::connect(grpc_url).await?;

    let (tx, rx) = tokio::sync::mpsc::channel(100);
    let outbound_stream = ReceiverStream::new(rx);
    let res = client.subscribe(outbound_stream);

    let worker_state = Arc::new(AgentState::new(WorkerStatus::Idle));

    let heartbeat_tx = tx.clone();
    let hearbeat_state = worker_state.clone();
    tokio::spawn(async move {
        let mut sys = sysinfo::System::new_all();
        loop {
            sys.refresh_all();
            let signal = WorkerSignal {
                worker_id: "test-worker-1".to_string(),
                available_memory: (sys.available_memory() / 1024 / 1024) as u32,
                total_memory: (sys.total_memory() / 1024 / 1024) as u32,
                cpu_percentage: sys.global_cpu_usage(),
                total_cores: sys.cpus().len() as u32,
                integrity_report: "OK".to_string(),
                status: hearbeat_state.get_status() as i32,
            };

            if let Err(e) = heartbeat_tx.send(signal).await {
                eprintln!("Failed heartbeat: {}", e);
                break;
            }
            tokio::time::sleep(tokio::time::Duration::from_secs(5)).await;
        }
    });
    let mut inbound_stream = res.await?.into_inner();
    let tasks_state = worker_state.clone();
    while let Some(signal) = inbound_stream.message().await? {
        match signal.event {
            Some(pb::brain_signal::Event::Task(task)) => {
                println!("Task: {}", task.task_id);
                tasks_state.set_status(WorkerStatus::Busy);
                tasks_state.perform_task(task);
            }
            Some(pb::brain_signal::Event::Ack(ack)) => {
                println!("Server Time: {}", ack.server_time);
            }
            None => {}
        }
    }

    Ok(())
}
