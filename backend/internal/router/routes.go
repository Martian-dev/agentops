package router

import (
	"context"
	"time"

	"github.com/Martian-dev/agentops/internal/agent"
	"github.com/Martian-dev/agentops/internal/db"
	"github.com/Martian-dev/agentops/internal/tools"
	"github.com/Martian-dev/agentops/internal/trace"
	"github.com/gofiber/fiber/v2"
)

// SetupRoutes configures all API routes
func SetupRoutes(app *fiber.App, toolRouter *tools.Router, traceEmitter *trace.ExecutorEmitter) {
	// API v1 routes
	v1 := app.Group("/api/v1")
	agentHandler := agent.NewHandlerWithDeps(db.Pool, toolRouter, traceEmitter)
	toolHandler := tools.NewAPIHandler(db.Pool, toolRouter)

	// Health check endpoint
	v1.Get("/health", healthCheckHandler)

	// Agent endpoints
	v1.Post("/agents", agentHandler.CreateAgent)
	v1.Get("/agents", agentHandler.ListAgents)
	v1.Get("/agents/:id", agentHandler.GetAgent)
	v1.Post("/agents/:id/run", agentHandler.RunAgent)
	v1.Get("/agents/:id/runs", agentHandler.ListAgentRuns)

	// Run endpoints
	v1.Get("/runs/:id/status", agentHandler.GetRunStatus)
	v1.Get("/runs/:id/trace", agentHandler.GetRunTrace)

	// Tool registry endpoints
	v1.Post("/tools", toolHandler.CreateTool)
	v1.Get("/tools", toolHandler.ListTools)
	v1.Get("/tools/:id", toolHandler.GetTool)
	v1.Put("/tools/:id", toolHandler.UpdateTool)
	v1.Delete("/tools/:id", toolHandler.DeleteTool)
	v1.Post("/tools/:id/test", toolHandler.TestTool)
}

// healthCheckHandler handles GET /api/v1/health
func healthCheckHandler(c *fiber.Ctx) error {
	// Create a context with timeout for DB ping
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	// Check database connectivity
	if err := db.Ping(ctx); err != nil {
		// Database is down, return 503 Service Unavailable
		return c.Status(fiber.StatusServiceUnavailable).JSON(agent.Envelope{
			Data: nil,
			Error: agent.ErrorBody{
				Code:    "db_unavailable",
				Message: "database connection failed",
			},
		})
	}

	// Database is up, return 200 OK
	return c.Status(fiber.StatusOK).JSON(agent.Envelope{
		Data:  "ok",
		Error: nil,
	})
}
