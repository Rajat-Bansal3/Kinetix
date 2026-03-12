package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	pb "Rajat-Bansal3/orchestrator/proto"

	"google.golang.org/grpc"
)

var (
    port  = flag.Int("port" , 50051 , "port the server is running on")
)

type Agent struct {
    ID string
    Stream pb.Orchestrator_SubscribeServer

    LastHeartbeat time.Time
    CPU float32
    TotalCores uint32
    TotalMemory uint32
    AvailableMemory uint32
    Status pb.WorkerStatus
}

type AgentRegistry struct {
    mu sync.RWMutex
    agents map[string]*Agent
}

type OrchestratorServer struct{
    pb.UnimplementedOrchestratorServer
    registry *AgentRegistry
}

func (r *AgentRegistry) Register(sig *pb.WorkerSignal , stream pb.Orchestrator_SubscribeServer) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.agents[sig.WorkerId] = &Agent{
        ID: sig.WorkerId,
        Stream: stream,
        LastHeartbeat: time.Now(),
        CPU: sig.CpuPercentage,
        TotalCores: sig.TotalCores,
        TotalMemory: sig.TotalMemory,
        AvailableMemory: sig.AvailableMemory,
        Status: sig.Status,
    };
}

func (r *AgentRegistry) Unregister(id string){
    r.mu.Lock()
    defer r.mu.Unlock()
    delete(r.agents , id)
}

func (r *AgentRegistry) GetStream(id string) (pb.Orchestrator_SubscribeServer, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()

    stream, ok := r.agents[id]
    return stream.Stream, ok
}
func (r *AgentRegistry) UpdateHealth(signal *pb.WorkerSignal) {
    r.mu.Lock()
    defer r.mu.Unlock()

    agent, ok := r.agents[signal.WorkerId]
    if !ok {
        return
    }

    agent.LastHeartbeat = time.Now()
    agent.CPU = signal.CpuPercentage
    agent.TotalCores = signal.TotalCores
    agent.TotalMemory = signal.TotalMemory
    agent.AvailableMemory = signal.AvailableMemory
    agent.Status = signal.Status
}
func (s *OrchestratorServer)Subscribe(stream pb.Orchestrator_SubscribeServer)error{
    var workerId string;
    defer func() {
        if workerId != "" {
            s.registry.Unregister(workerId)
            log.Printf("Worker %s disconnected", workerId)
        }
    }()
    for {
        workerSignal, err := stream.Recv()
        if err == io.EOF {
            if workerId != ""{
                s.registry.Unregister(workerId)
            }
            return nil
        }
        if err != nil {
            return err
        }
        if workerId == ""{
            workerId = workerSignal.WorkerId
            s.registry.Register(workerSignal , stream)
        }
        log.Printf(
            "Worker %s | CORES %d | CPU %.2f | Total_Memory %d | Available_Memory %d | Status %v",
            workerSignal.WorkerId,
            workerSignal.TotalCores,
            workerSignal.CpuPercentage,
            workerSignal.TotalMemory,
            workerSignal.AvailableMemory,
            workerSignal.Status,
        )
        if workerId != "" {s.registry.UpdateHealth(workerSignal)}
        if err := stream.Send(&pb.BrainSignal{
	        Event: &pb.BrainSignal_Ack{
		        Ack: &pb.HeartbeatAck{
			        ServerTime: time.Now().Unix(),
        		},
	        },
        });
        err != nil {
	        return err
        }
    }
}

func main (){
    flag.Parse()
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
    s := &OrchestratorServer{
        registry: &AgentRegistry{
            agents: make(map[string]*Agent),
        },
    }
    pb.RegisterOrchestratorServer(grpcServer, s)
    log.Printf("Server running on port %d", *port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal(err)
	}
}