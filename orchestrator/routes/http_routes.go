package routes

import (
	rh "Rajat-Bansal3/orchestrator/internals/redis_handler"
	pb "Rajat-Bansal3/orchestrator/proto"
	"encoding/base64"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"google.golang.org/protobuf/encoding/protojson"
)

type Payload struct {
    StringWasm string `json:"string_wasm"`
    Binary     string `json:"binary"`
}

type CreateJobRequest struct {
    Type      string            `json:"type"`
    Priority  int               `json:"priority"`
    Payload   Payload           `json:"payload"`
    Metadata  map[string]string `json:"metadata"`
    FuelLimit uint64            `json:"fuel_limit"`
}

func SetupHttpRoutes(e *echo.Echo, redis *rh.Handler) {
    api := e.Group("/api")

    api.POST("/job", func(c *echo.Context) error {
        req := new(CreateJobRequest)
        if err := c.Bind(req); err != nil {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "bad request body"})
        }
        if req.Type == "" {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "type missing"})
        }
        if req.Payload.StringWasm == "" && req.Payload.Binary == "" {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "no wasm provided"})
        }

        var wasmBytes []byte
        if req.Payload.Binary != "" {
            decoded, err := base64.StdEncoding.DecodeString(req.Payload.Binary)
            if err != nil {
                return c.JSON(http.StatusBadRequest, map[string]string{"error": "binary not valid base64"})
            }
            wasmBytes = decoded
        } else {
            wasmBytes = []byte(req.Payload.StringWasm)
        }



        task := &pb.TaskAssignment{
            TaskId:     uuid.NewString(),
            WasmBinary: wasmBytes,
            FuelLimit:  req.FuelLimit,
            Env:        req.Metadata,
        }

        taskBytes, err := protojson.Marshal(task)
        if err != nil {
            return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encode failed"})
        }

        if err := redis.Redis.LPush(c.Request().Context(), "tasks:pending", taskBytes).Err(); err != nil {
            log.Printf("redis lpush: %v", err)
            return c.JSON(http.StatusInternalServerError, map[string]string{"error": "queue down"})
        }

        return c.JSON(http.StatusAccepted, map[string]string{
            "message": "queued",
            "job_id":  task.TaskId,
        })
    })
}