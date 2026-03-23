package main

import (
	"log"
	"os"

	"github.com/Martian-dev/agentops/internal/agent"
	"github.com/Martian-dev/agentops/internal/db"
	"github.com/Martian-dev/agentops/internal/router"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	// Get database URL from environment
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}

	// Initialize database connection
	if err := db.InitDB(databaseURL); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	log.Println("✓ Connected to PostgreSQL")

	// Create Fiber app with custom configuration
	app := fiber.New(fiber.Config{
		AppName:      "AgentOps API",
		ErrorHandler: defaultErrorHandler,
	})

	// Middleware
	app.Use(recover.New())

	// Setup routes
	router.SetupRoutes(app)

	// Health check for container orchestration
	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	// 404 handler
	app.Use(func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(agent.Envelope{
			Data: nil,
			Error: agent.ErrorBody{
				Code:    "not_found",
				Message: "endpoint not found",
			},
		})
	})

	// Start server
	port := ":8080"
	log.Printf("🚀 Starting server on %s", port)
	if err := app.Listen(port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// defaultErrorHandler handles errors globally
func defaultErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	message := "Internal Server Error"

	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		message = e.Message
	}

	log.Printf("ERROR: %v", err)

	return c.Status(code).JSON(agent.Envelope{
		Data: nil,
		Error: agent.ErrorBody{
			Code:    "request_error",
			Message: message,
		},
	})
}
