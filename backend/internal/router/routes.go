package router

import (
	"context"
	"time"

	"github.com/Martian-dev/agentops/internal/agent"
	"github.com/Martian-dev/agentops/internal/db"
	"github.com/gofiber/fiber/v2"
)

// SetupRoutes configures all API routes
func SetupRoutes(app *fiber.App) {
	// API v1 routes
	v1 := app.Group("/api/v1")
	agentHandler := agent.NewHandler()

	// Health check endpoint
	v1.Get("/health", healthCheckHandler)

	// Agent endpoints
	v1.Post("/agents", agentHandler.CreateAgent)
	v1.Get("/agents", agentHandler.ListAgents)
	v1.Get("/agents/:id", agentHandler.GetAgent)
	v1.Post("/agents/:id/run", agentHandler.RunAgent)
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
