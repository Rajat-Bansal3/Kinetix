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
	"github.com/google/uuid"
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
type Payload struct {
    StringWasm string `json:"string_wasm"`
    Binary     []byte `json:"binary"`
}
type Job struct {
    JobID string `json:"job_id"`
    Type string `json:"type" validate:"required"`
    Priority int `json:"priority" validate:"min=0,max=10"`
    Payload Payload `json:"payload" validate:"required"`
    Metadata map[string]string `json:"metadata"`
}

func (s *OrchestratorServer)AssignTasks(ctx context.Context , rdb *redis.Client , task Job)error {
    log.Printf("task %s" , task.JobID)
    s.registry.mu.Lock()
    if len(s.registry.ids) == 0 {
        s.registry.mu.Unlock()
        return fmt.Errorf("no agents available to take the job")
    }
    if task.Payload.StringWasm == "" && len(task.Payload.Binary) == 0 {
        return fmt.Errorf("either string_wasm or binary must be provided")
    }
    targetId := s.registry.ids[s.registry.next]
    agent := s.registry.agents[targetId]
    s.registry.next = (s.registry.next + 1) % len(s.registry.ids)

    s.registry.mu.Unlock()
    fmt.Println(task.Payload.StringWasm)
    payloadBytes := task.Payload.StringWasm
    _ , err := rdb.HSet(ctx, "job_owners", task.JobID, targetId).Result()

    if err != nil {
        return fmt.Errorf("error setting owner hash")
    }
    println(payloadBytes)
    return agent.Stream.Send(&pb.BrainSignal{
        Event: &pb.BrainSignal_Task{
            Task: &pb.TaskAssignment{
                TaskId:     task.JobID,
                WasmBinary: []byte(payloadBytes),
            },
        },
    })
}
func (s *OrchestratorServer) reclaimJob (ctx context.Context , rdb *redis.Client , id string){
    processingJobs, err := rdb.LRange(ctx, "tasks:processing", 0, -1).Result()
    if err != nil {
        return
    }
    for _, jobJson := range processingJobs {
        var job Job
        if err := json.Unmarshal([]byte(jobJson), &job); err != nil {
            continue
        }
        owner , _ := rdb.HGet(ctx , "job_owners" , job.JobID).Result()
        if owner == id {
            rdb.LRem(ctx, "tasks:processing", 1, jobJson)
            rdb.LPush(ctx, "tasks:pending", jobJson)
            rdb.HDel(ctx, "job_owners", job.JobID)
        }
    }
}

func (s *OrchestratorServer) reaper (ctx context.Context , rdb *redis.Client){
    ticker := time.NewTicker(10 * time.Second)
    for {
        select {
        case <- ctx.Done():
            return
        case <- ticker.C :
           deadAgents := []string{}
            s.registry.mu.RLock()
            for id, agent := range s.registry.agents {
                if time.Since(agent.LastHeartbeat) > 15*time.Second {
                    deadAgents = append(deadAgents, id)
                }
            }
            s.registry.mu.RUnlock()

            for _, id := range deadAgents {
                s.registry.Unregister(id) 
                s.reclaimJob(ctx, rdb, id)
            }
        }
    }
}

func (r *AgentRegistry) Register(sig *pb.WorkerSignal , stream pb.Orchestrator_SubscribeServer) {
    r.mu.Lock()
    defer r.mu.Unlock()
    println("Registered")
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

func (r *AgentRegistry) Unregister(id string) {
    r.mu.Lock()
    defer r.mu.Unlock()

    agent, ok := r.agents[id]
    if !ok {
        return
    }
    if time.Since(agent.LastHeartbeat) < 15*time.Second {
        return
    }

    delete(r.agents, id)
    for i, v := range r.ids {
        if v == id {
            r.ids = append(r.ids[:i], r.ids[i+1:]...)
            if len(r.ids) == 0 || r.next >= len(r.ids) {
                r.next = 0
            }
            break
        }
    }
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
    var workerId = uuid.New().String();
    defer func() {        
            s.registry.Unregister(workerId)
            log.Printf("Worker %s disconnected", workerId)
    }()
    registered := false
    for {
        workerSignal, err := stream.Recv()
        if err == io.EOF {
            log.Printf("Worker %s closed connection gracefully", workerId)
            return nil
        }
        if err != nil {
            return err
        }
        workerSignal.WorkerId = workerId
        if !registered {
            s.registry.Register(workerSignal , stream)
            registered = true
        }else {
            s.registry.UpdateHealth(workerSignal)
        }
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

func (s *OrchestratorServer)debug_loop() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        <-ticker.C
        s.registry.mu.RLock()
        
        fmt.Println("\n--- 🔍 Orchestrator Internal State ---")
        fmt.Printf("Total Registered Agents: %d\n", len(s.registry.agents))
        fmt.Printf("Round-Robin Next Index:  %d\n", s.registry.next)
        
        if len(s.registry.agents) > 0 {
            fmt.Printf("%-15s | %-10s | %-6s | %-8s\n", "WORKER ID", "STATUS", "CPU %", "MEM AVAIL")
            for id, agent := range s.registry.agents {
                fmt.Printf("%-15s | %-10v | %-6.2f | %-8d MB\n", 
                    id, 
                    agent.Status, 
                    agent.CPU, 
                    agent.AvailableMemory,
                )
            }
        } else {
            fmt.Println("No agents currently connected.")
        }
        fmt.Println("--------------------------------------")
        
        s.registry.mu.RUnlock()
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
            if len(s.registry.agents) < 1 {
                continue
            }
            jobData , err := redisSvc.Redis.BRPopLPush(context.Background() , "tasks:pending", "tasks:processing",5*time.Second ).Result()
            if err != nil {
                if err == redis.Nil {
                    continue
                }
                log.Printf("Redis Error: %v", err)
                time.Sleep(time.Second) 
                continue
            }
            job := Job{}
            println(jobData)
            if err := json.Unmarshal([]byte(jobData), &job); err != nil {
                log.Printf("Payload corruption: %v", err)
                continue
            }
            s.AssignTasks(rdb.Context() , rdb , job)
        }
    }()
    go s.reaper(context.Background() , rdb)
    go s.debug_loop()
    e := echo.New()
    
    
    routes.SetupHttpRoutes(e, redisSvc)

    log.Printf("HTTP server running on port %d", *port_http)
    
    if err := e.Start(fmt.Sprintf(":%d" , *port_http)); err != nil {
        log.Fatal("failed to start server", "error", err)
    }

}