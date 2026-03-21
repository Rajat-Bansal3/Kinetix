package main

//imports
import (
	"context"
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
	"google.golang.org/protobuf/encoding/protojson"
)

//flags
var (
    port_grpc  = flag.Int("port-grpc" , 50051 , "port the GRPC server is running on")
    port_http  = flag.Int("port-http" , 5000 , "port the HTTP server is running on")
    redis_url  = flag.String("redis-url" , "localhost:6379" , "set the redis url defaults to localhost:6379")
)


//structs 
type Agent struct {
    ID string
    Stream pb.Orchestrator_SubscribeServer

    LastHeartbeat time.Time
    CPU float32
    TotalCores uint32
    TotalMemory uint64
    AvailableMemory uint64
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
    rdb      *redis.Client

}
type Payload struct {
    StringWasm string `json:"string_wasm"`
    Binary     []byte `json:"binary"`
}


// orchestration functions
func (s *OrchestratorServer)AssignTasks(ctx context.Context , rdb *redis.Client , task *pb.TaskAssignment)error {
    fmt.Printf("task:assignTask: %+v\n", task)
    log.Printf("task %s" , task.TaskId)
    s.registry.mu.Lock()
    if len(s.registry.ids) == 0 {
        s.registry.mu.Unlock()
        return fmt.Errorf("no agents available to take the job")
    }
    if len(task.WasmBinary) == 0 {
        s.registry.mu.Unlock()
        return fmt.Errorf("binary must be provided")
    }
    targetId := s.registry.ids[s.registry.next]
    agent := s.registry.agents[targetId]
    s.registry.next = (s.registry.next + 1) % len(s.registry.ids)
    s.registry.mu.Unlock()

    payloadBytes := task.WasmBinary
    _ , err := rdb.HSet(ctx, "job_owners", task.TaskId, targetId).Result()
    taskMarshal , _ := protojson.Marshal(task)
    _, e := rdb.HSet(ctx, "jobs", task.TaskId , taskMarshal).Result()

    if (err != nil) || (e != nil) {
        return fmt.Errorf("error setting owner hash or setting job details")
    }
    println("Reached here")
    return agent.Stream.Send(&pb.BrainSignal{
        Event: &pb.BrainSignal_Task{
            Task: &pb.TaskAssignment{
                TaskId:     task.TaskId,
                WasmBinary: []byte(payloadBytes),
                FuelLimit:  task.FuelLimit,
                Env: task.Env,
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
        var job pb.TaskAssignment
        if err := protojson.Unmarshal([]byte(jobJson), &job); err != nil {
            continue
        }
        owner , _ := rdb.HGet(ctx , "job_owners" , job.TaskId).Result()
        if owner == id {
            rdb.LRem(ctx, "tasks:processing", 1, jobJson)
            rdb.LPush(ctx, "tasks:pending", jobJson)
            rdb.HDel(ctx, "job_owners", job.TaskId)
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
func(s *OrchestratorServer) Result(sig *pb.WorkerSignal){
    result := sig.Result
    ctx := context.Background()
    log.Printf("Task %s completed | status: %v | exit_code: %d | error: %s",
        result.TaskId, result.Status, result.ExitCode, result.Error)
    switch result.Status{
        case pb.TaskStatus_SUCCESS:
            s.rdb.LRem(ctx , "tasks:processing" , 1 , result.TaskId)
            s.rdb.HDel(ctx , "job_owners" , result.TaskId)
            s.rdb.HSet(ctx , "job_completed", result.TaskId, result.ExitCode)
            s.rdb.HDel(ctx , "jobs", result.TaskId)

        case pb.TaskStatus_REJECTED , pb.TaskStatus_FAILED:
            s.rdb.LRem(ctx , "tasks:processing" , 1 , result.TaskId)
            s.rdb.HDel(ctx , "job_owners" , result.TaskId)
            jobJson, err := s.rdb.HGet(ctx, "jobs", result.TaskId).Result()
            if err != nil {
                log.Printf("could not find job data for %s, job lost", result.TaskId)
                return
            }
            s.rdb.LPush(ctx, "tasks:pending", jobJson)

    }
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
            if workerSignal.Result != nil{
                s.Result(workerSignal)
            }
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

func main (){
    flag.Parse()
    //service initialisation
    rdb := redis.NewClient(&redis.Options{
        Addr:     *redis_url,
        Password: "",
        DB:       0,
    })
    redisSvc := rh.NewHandler(rdb)
    s := &OrchestratorServer{
            registry: &AgentRegistry{
                agents: make(map[string]*Agent),
            },
            rdb: rdb,
        }

    //main grpc loop
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
    //jobs reclaim loop
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
            job := &pb.TaskAssignment{}
            if er := protojson.Unmarshal([]byte(jobData), job); er != nil {
                fmt.Println("error unmarshling job:main")
            }
            fmt.Printf("task:main: %+v\n", job)

            if err := s.AssignTasks(rdb.Context(), rdb, job); err != nil {
                log.Printf("AssignTasks failed: %v", err)
            }
        }
    }()
    //agents unregistering loop
    go s.reaper(context.Background() , rdb)
    e := echo.New()
    
    //route for job ingress
    routes.SetupHttpRoutes(e, redisSvc)

    // main http server
    if err := e.Start(fmt.Sprintf(":%d" , *port_http)); err != nil {
        log.Fatal("failed to start server", "error", err)
    }

}