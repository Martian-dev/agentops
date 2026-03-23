# AgentOps Backend API

A Go-based REST API built with [Fiber](https://gofiber.io/) for managing AI agents and their tools.

## Project Structure

```
backend/
├── cmd/
│   └── api/
│       └── main.go          # Application entry point
├── internal/
│   ├── db/
│   │   └── postgres.go      # Database connection management
│   ├── router/
│   │   └── routes.go        # Route definitions
│   ├── agent/
│   │   └── agent.go         # Agent domain models
│   └── tools/
│       └── tool.go          # Tool domain models
├── migrations/              # Database migration files
├── go.mod                   # Go module definition
├── go.sum                   # Dependency checksums
└── Dockerfile               # Multi-stage build for production
```

## Prerequisites

- Go 1.22 or later
- PostgreSQL 16+
- Docker & Docker Compose (for containerized development)

## Quick Start

### Local Development

1. **Set up environment variables**:

   ```bash
   export DATABASE_URL="postgres://agentops:agentops@localhost:5432/agentops?sslmode=disable"
   ```

2. **Install dependencies**:

   ```bash
   go mod download
   ```

3. **Run database migrations**:

   ```bash
   # Install migrate if not already installed
   brew install golang-migrate  # macOS
   # or download from https://github.com/golang-migrate/migrate/releases

   migrate -path ./migrations -database "$DATABASE_URL" up
   ```

4. **Start the API**:

   ```bash
   go run ./cmd/api/main.go
   ```

   The API will be available at `http://localhost:8080`

### Docker Compose (Recommended)

```bash
cd ../infra
docker-compose up
```

This will:

- Start PostgreSQL with initial migrations
- Start Redis cache
- Start Qdrant vector database
- Start the backend API on port 8080

## API Endpoints

### Health Check

```http
GET /api/v1/health
```

**Response (200 OK)**:

```json
{
  "data": "ok",
  "error": null
}
```

**Response (503 Service Unavailable - DB down)**:

```json
{
  "data": "",
  "error": "database connection failed"
}
```

**Details**:

- Verifies API is running
- Pings PostgreSQL to ensure database connectivity
- Returns 200 if database is accessible
- Returns 503 if database connection fails

### Health Check (Container Orchestration)

```http
GET /healthz
```

**Response (200 OK)**: Empty response for orchestration health checks

---

## Development

### Building from Source

```bash
go build -o agentops-api ./cmd/api
./agentops-api
```

### Running Tests

```bash
go test ./...
```

### Code Organization

- **`cmd/api`**: Application entry point and initialization
- **`internal/db`**: Database connection pooling and utilities
- **`internal/router`**: HTTP route definitions and handlers
- **`internal/agent`**: Agent domain logic and models
- **`internal/tools`**: Tool domain logic and models

### Dependencies

- `github.com/gofiber/fiber/v2` - High-performance web framework
- `github.com/jackc/pgx/v5` - PostgreSQL driver with connection pooling
- `golang.org/x/*` - Go standard extensions

### Adding Dependencies

```bash
go get github.com/package/v2
go mod tidy
```

## Configuration

### Environment Variables

| Variable       | Description                  | Required | Default                  |
| -------------- | ---------------------------- | -------- | ------------------------ |
| `DATABASE_URL` | PostgreSQL connection string | Yes      | -                        |
| `REDIS_URL`    | Redis connection URL         | Optional | `redis://localhost:6379` |
| `QDRANT_URL`   | Qdrant vector DB URL         | Optional | `http://localhost:6333`  |

### Database Connection String Format

```
postgres://[user[:password]@][netloc][:port][/dbname][?param1=value1&...]
```

**Example**:

```
postgres://agentops:agentops@localhost:5432/agentops?sslmode=disable
```

## Deployment

### Building Docker Image

```bash
docker build -t agentops-backend:latest .
```

### Running in Docker

```bash
docker run -e DATABASE_URL="..." -p 8080:8080 agentops-backend:latest
```

## Troubleshooting

### Database Connection Issues

**Error: "unable to parse DATABASE_URL"**

- Verify the connection string format
- Check PostgreSQL is running: `psql -U agentops -d agentops -c "SELECT 1;"`

**Error: "unable to ping database"**

- Ensure PostgreSQL service is running
- Check network connectivity
- Verify database credentials

### Port Already in Use

```bash
# Find process using port 8080
lsof -i :8080
# Kill the process
kill -9 <PID>
```

## Contributing

1. Create a feature branch: `git checkout -b feature/my-feature`
2. Make changes and test locally
3. Run migrations verification
4. Commit with clear messages
5. Push and create a pull request

## License

See LICENSE file in the project root.
