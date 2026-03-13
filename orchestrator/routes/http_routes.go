package routes

import (
	rh "Rajat-Bansal3/orchestrator/internals/redis_handler"
	"encoding/json"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type CreateJobRequest struct {
    JobID string 
    Type string `json:"type" validate:"required"`
    Priority int `json:"priority" validate:"min=0,max=10"`
    Payload map[string]interface{} `json:"payload" validate:"required"`
    Metadata map[string]string `json:"metadata"`
}


func SetupHttpRoutes (e *echo.Echo , redis *rh.Handler){
    api := e.Group("/api")

    api.POST("/job" , func(c *echo.Context) error {
        req := new(CreateJobRequest)
        if err := c.Bind(req); err != nil {
            return c.JSON(http.StatusBadRequest , map[string]string{"error": "Invalid format"})
        }
        
        req.JobID = uuid.NewString() 
        if req.Type == "" || len(req.Payload) == 0 {
           return c.JSON(http.StatusBadRequest, map[string]string{
                "error": "Validation failed: 'type' and 'payload' are required",
            })
        }

        taskBytes, err := json.Marshal(req)
        if err != nil {
            return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to encode task"})
        }

        err = redis.Redis.LPush(c.Request().Context(), "tasks:pending", taskBytes).Err()

        if err != nil {
            log.Printf("Redis error: %v", err)
            return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Queue unavailable"})
        }
        return c.JSON(http.StatusAccepted, map[string]string{
            "message": "Job queued successfully",
            "job_id":  req.JobID,
        })
    })

}