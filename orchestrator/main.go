package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	pb "Rajat-Bansal3/orchestrator/proto"

	rh "Rajat-Bansal3/orchestrator/internals/redis_handler"

	routes "Rajat-Bansal3/orchestrator/routes"

	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v5"
	"google.golang.org/grpc"
)

var (
    port_grpc  = flag.Int("port-grpc" , 50051 , "port the GRPC server is running on")
    port_http  = flag.Int("port-http" , 5000 , "port the HTTP server is running on")
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
    ids    []string 
    next   int
}

type OrchestratorServer struct{
    pb.UnimplementedOrchestratorServer
    registry *AgentRegistry
}
type Job struct {
    JobID string 
    Type string `json:"type" validate:"required"`
    Priority int `json:"priority" validate:"min=0,max=10"`
    Payload map[string]interface{} `json:"payload" validate:"required"`
    Metadata map[string]string `json:"metadata"`
}

func (s *OrchestratorServer)AssignTasks(task Job)error {
    log.Printf("task %s" , task.JobID)
    s.registry.mu.Lock()
    if len(s.registry.ids) == 0 {
        s.registry.mu.Unlock()
        return fmt.Errorf("no agents available to take the job")
    }
    targetId := s.registry.ids[s.registry.next]
    agent := s.registry.agents[targetId]
    s.registry.next = (s.registry.next + 1) % len(s.registry.ids)

    s.registry.mu.Unlock()

    payloadBytes, _ := json.Marshal(task)

    return agent.Stream.Send(&pb.BrainSignal{
        Event: &pb.BrainSignal_Task{
            Task: &pb.TaskAssignment{
                TaskId:     task.JobID,
                WasmBinary: payloadBytes,
            },
        },
    })
}

func (r *AgentRegistry) Register(sig *pb.WorkerSignal , stream pb.Orchestrator_SubscribeServer) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if _ , exists := r.agents[sig.WorkerId]; !exists {
        r.ids = append(r.ids, sig.WorkerId)
    }
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
    rdb := redis.NewClient(&redis.Options{
        Addr:     "localhost:6379",
        Password: "",
        DB:       0,
    })

    redisSvc := rh.NewHandler(rdb)
    s := &OrchestratorServer{
            registry: &AgentRegistry{
                agents: make(map[string]*Agent),
            },
        }
	go func(){
        lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *port_grpc))
        if err != nil {
            log.Fatalf("failed to listen: %v", err)
        }
        
        grpcServer := grpc.NewServer()
        
        pb.RegisterOrchestratorServer(grpcServer, s)
        log.Printf("GRPC server running on port %d", *port_grpc)
        if err := grpcServer.Serve(lis); err != nil {
            log.Fatal(err)
        }
    }()
    go func(){
        for{
            jobData , err := redisSvc.Redis.BRPop(context.Background() , 5*time.Second , "tasks:pending").Result()
            if err != nil {
                if err == redis.Nil {
                    continue
                }
                log.Printf("Redis Error: %v", err)
                time.Sleep(time.Second) 
                continue
            }
            job := Job{}
            if err := json.Unmarshal([]byte(jobData[1]), &job); err != nil {
                log.Printf("Payload corruption: %v", err)
                continue
            }
            s.AssignTasks(job)
        }
    }()
    e := echo.New()
    
    
    routes.SetupHttpRoutes(e, redisSvc)

    log.Printf("HTTP server running on port %d", *port_http)
    
    if err := e.Start(fmt.Sprintf(":%d" , *port_http)); err != nil {
        log.Fatal("failed to start server", "error", err)
    }

}