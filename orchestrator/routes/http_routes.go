package routes

import (
	rh "Rajat-Bansal3/orchestrator/internals/redis_handler"

	"github.com/labstack/echo/v5"
)

func SetupHttpRoutes (e *echo.Echo , redis *rh.Handler){
    api := e.Group("/api")

    api.POST("/job" , func(c *echo.Context) error {

        return echo.ErrInternalServerError
    })

}