package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"

	"github.com/forgelab/backend/internal/auth"
	"github.com/forgelab/backend/internal/config"
	"github.com/forgelab/backend/internal/crypto"
	"github.com/forgelab/backend/internal/database"
	"github.com/forgelab/backend/internal/docker"
	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/middleware"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/network"
	"github.com/forgelab/backend/internal/queue"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
	ws "github.com/forgelab/backend/internal/websocket"
)

func main() {
	// Load .env file if it exists
	if err := godotenv.Load("../.env"); err != nil {
		godotenv.Load(".env")
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Configure logging
	var logLevel slog.Level
	switch cfg.Log.Level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	// Connect to database
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := database.Connect(ctx, cfg.Database.URL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Connect to Redis
	redisClient, err := database.ConnectRedis(ctx, cfg.Redis.URL)
	if err != nil {
		slog.Warn("redis connection failed — running with fallback", "error", err)
	}
	if redisClient != nil {
		defer redisClient.Close()
	}

	// Initialize Docker Client
	dockerCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		slog.Warn("docker client initialization warning", "error", err)
	}
	if dockerCli != nil {
		defer dockerCli.Close()
	}

	// Initialize helpers and security primitives
	var allowedRoots []string
	if cfg.Docker.AllowedSourceRoots != "" {
		allowedRoots = strings.Split(cfg.Docker.AllowedSourceRoots, ",")
	}
	pathValidator := security.NewPathValidatorWithMapping(
		allowedRoots,
		cfg.Docker.HostSourceRoot,
		cfg.Docker.ContainerSourceRoot,
	)
	portManager := network.NewPortManager(10000, 60000)

	encryptor, err := crypto.NewEncryptor(cfg.Encryption.Key)
	if err != nil {
		slog.Error("failed to initialize secret encryptor", "error", err)
		os.Exit(1)
	}

	// Initialize services
	jwtManager := auth.NewJWTManager(
		cfg.JWT.Secret,
		cfg.JWT.AccessTokenExpiry,
		cfg.JWT.RefreshTokenExpiry,
	)

	sourceService := services.NewSourceService(pool, cfg.Docker.SourcesDir, encryptor)
	githubService := services.NewGitHubService(pool, encryptor, cfg.GitHub, redisClient)
	userService := services.NewUserService(pool, jwtManager)
	serviceService := services.NewServiceService(pool)
	projectService := services.NewProjectService(pool, pathValidator, sourceService, githubService)
	projectService.SetServiceService(serviceService)
	deploymentService := services.NewDeploymentService(pool)
	secretService := services.NewSecretService(pool, encryptor, projectService)
	deploymentService.SetSecretService(secretService)

	// Initialize WebSocket Hub
	wsHub := ws.NewHub(jwtManager, projectService, deploymentService, redisClient, cfg.App.AllowedOriginsList()...)
	wsHub.SetServiceDeploymentResolver(deploymentService)
	wsHub.SetServiceResolver(serviceService)

	// Initialize Docker Engine
	dockerEngine := docker.NewEngine(
		dockerCli,
		projectService,
		deploymentService,
		secretService,
		sourceService,
		githubService,
		portManager,
		pathValidator,
		wsHub,
		cfg.Docker.WorkDir,
	)
	dockerEngine.SetServiceService(serviceService)
	dockerEngine.SetLocalBuildMode(cfg.Docker.LocalBuildMode)
	dockerEngine.SetMaxConcurrentBuilds(cfg.Docker.MaxConcurrentBuilds)
	defer dockerEngine.StopAllLogCollectors()

	// Initialize Redis deployment queue & worker
	var deployQueue *queue.DeploymentQueue
	if redisClient != nil {
		deployQueue = queue.NewDeploymentQueue(redisClient)
		deployQueue.SetWorkerCount(cfg.Docker.QueueWorkerCount)
		deployQueue.SetTerminalChecker(func(ctx context.Context, job queue.Job) (bool, error) {
			if job.Type == queue.JobTypeServiceDeployment {
				sd, err := deploymentService.GetServiceDeployment(ctx, job.ID)
				if err != nil {
					return false, err
				}
				return models.IsDeploymentTerminalStatus(sd.Status), nil
			}
			dep, err := deploymentService.GetDeployment(ctx, job.ID)
			if err != nil {
				return false, err
			}
			return models.IsDeploymentTerminalStatus(dep.Status), nil
		})
		deployQueue.StartJobWorker(ctx, func(workerCtx context.Context, job queue.Job) error {
			if job.Type == queue.JobTypeServiceDeployment {
				return dockerEngine.ExecuteServiceDeployment(workerCtx, job.ID)
			}
			return dockerEngine.ExecuteDeployment(workerCtx, job.ID)
		})
		defer deployQueue.Stop()
	}

	// Startup reconciliation for orphaned / missing deployments
	reconcileFn := func() {
		if deployQueue != nil {
			if reconciled, err := deploymentService.ReconcileOrphanedDeploymentsWithQueue(ctx, deployQueue, 10*time.Minute, queue.DefaultMaxRetries); err != nil {
				slog.Warn("failed to reconcile orphaned deployments", "error", err)
			} else if reconciled > 0 {
				slog.Info("reconciled orphaned deployments with queue recovery", "count", reconciled)
			}
		} else {
			if reconciled, err := deploymentService.ReconcileOrphanedDeployments(ctx, 30*time.Minute); err != nil {
				slog.Warn("failed to reconcile orphaned deployments on startup", "error", err)
			} else if reconciled > 0 {
				slog.Info("reconciled orphaned deployments on startup", "count", reconciled)
			}
		}
	}

	reconcileFn()
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcileFn()
			}
		}
	}()

	// Initialize handlers
	oauthService := services.NewOAuthService(cfg.Google, cfg.GitHub, redisClient)
	authHandler := handlers.NewAuthHandler(userService, oauthService, cfg.App.FrontendURL, cfg.App.CookieSecure)
	projectHandler := handlers.NewProjectHandler(projectService, deploymentService, dockerEngine, deployQueue)
	serviceHandler := handlers.NewServiceHandler(serviceService, projectService, deploymentService, dockerEngine, deployQueue)
	envHandler := handlers.NewEnvHandler(secretService)
	integrationHandler := handlers.NewIntegrationHandler(githubService, cfg.App.FrontendURL)
	sourceHandler := handlers.NewSourceHandler(sourceService, pathValidator)

	// Setup router
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(middleware.RequestLogger)
	r.Use(middleware.CORS(cfg.App.CORSAllowedOrigins))
	r.Use(chimiddleware.Recoverer)

	// Health check (unauthenticated)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok", "service": "forgelab"}`))
	})

	// WebSocket endpoint
	r.Get("/api/ws", wsHub.ServeWS)

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.ContentTypeJSON)

		// Public GitHub OAuth integration callback
		r.Get("/integrations/github/callback", integrationHandler.GitHubCallback)

		// Agent session validation (token-authenticated by agent)
		r.Post("/sources/agent/session/validate", sourceHandler.ValidateAgentSession)

		// Auth routes (public)
		r.Route("/auth", func(r chi.Router) {
			r.Post("/register", authHandler.Register)
			r.Post("/login", authHandler.Login)
			r.Post("/refresh", authHandler.Refresh)
			r.Post("/logout", authHandler.Logout)

			// OAuth routes (public)
			r.Get("/google", authHandler.GoogleLogin)
			r.Get("/google/callback", authHandler.GoogleCallback)
			r.Get("/github", authHandler.GitHubLogin)
			r.Get("/github/callback", authHandler.GitHubCallback)

			// Protected auth routes
			r.Group(func(r chi.Router) {
				r.Use(middleware.AuthMiddleware(jwtManager))
				r.Get("/me", authHandler.Me)
			})
		})

		// Protected routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(jwtManager))

			// Source Management & Uploads
			r.Route("/sources", func(r chi.Router) {
				r.Post("/upload", sourceHandler.Upload)
				r.Post("/agent/session", sourceHandler.CreateAgentSession)
				r.Post("/agent/register", sourceHandler.RegisterAgentSource)
				r.Post("/local/validate", sourceHandler.ValidateLocalPath)
				r.Get("/local/validate/{sessionId}", sourceHandler.GetLocalValidationStatus)
				r.Get("/{id}", sourceHandler.GetStatus)
				r.Delete("/{id}", sourceHandler.Delete)
			})

			// Integrations (GitHub Repository Access)
			r.Route("/integrations", func(r chi.Router) {
				r.Route("/github", func(r chi.Router) {
					r.Get("/", integrationHandler.GetGitHubStatus)
					r.Get("/connect", integrationHandler.ConnectGitHub)
					r.Post("/connect", integrationHandler.ConnectGitHub)
					r.Post("/disconnect", integrationHandler.DisconnectGitHub)
					r.Get("/repositories", integrationHandler.ListRepositories)
					r.Get("/repositories/{owner}/{repo}/branches", integrationHandler.ListBranches)
					r.Get("/repositories/{owner}/{repo}/detect", integrationHandler.DetectRepository)
					r.Post("/repositories/{owner}/{repo}/detect", integrationHandler.DetectRepository)
				})
			})

			// Projects
			r.Route("/projects", func(r chi.Router) {
				r.Post("/", projectHandler.Create)
				r.Get("/", projectHandler.List)
				r.Get("/{id}", projectHandler.Get)
				r.Patch("/{id}", projectHandler.Update)
				r.Delete("/{id}", projectHandler.Delete)

				// Service Lifecycle Controls & Listing
				r.Get("/{id}/services", serviceHandler.List)
				r.Post("/{id}/services/{serviceId}/deploy", serviceHandler.Deploy)
				r.Post("/{id}/services/{serviceId}/rollback", serviceHandler.Rollback)
				r.Get("/{id}/services/{serviceId}/deployments", serviceHandler.ListDeployments)
				r.Get("/{id}/services/{serviceId}/deployments/{deploymentId}", serviceHandler.GetDeployment)
				r.Get("/{id}/services/{serviceId}/deployments/{deploymentId}/logs", serviceHandler.GetLogs)
				r.Post("/{id}/services/{serviceId}/stop", serviceHandler.Stop)
				r.Post("/{id}/services/{serviceId}/start", serviceHandler.Start)
				r.Post("/{id}/services/{serviceId}/restart", serviceHandler.Restart)
				r.Patch("/{id}/services/{serviceId}/resources", serviceHandler.UpdateResources)

				// Application Lifecycle Controls
				r.Post("/{id}/stop", projectHandler.Stop)
				r.Post("/{id}/start", projectHandler.Start)
				r.Post("/{id}/restart", projectHandler.Restart)
				r.Post("/{id}/rollback", projectHandler.Rollback)

				// Environment Variables / Secrets
				r.Get("/{id}/env", envHandler.List)
				r.Post("/{id}/env", envHandler.Set)
				r.Delete("/{id}/env/{key}", envHandler.Delete)

				// Deployments
				r.Post("/{id}/deployments", projectHandler.Deploy)
				r.Get("/{id}/deployments", projectHandler.ListDeployments)
				r.Get("/{id}/deployments/{deploymentId}", projectHandler.GetDeployment)
				r.Get("/{id}/deployments/{deploymentId}/services", projectHandler.ListServiceDeployments)
				r.Get("/{id}/deployments/{deploymentId}/logs", projectHandler.GetDeploymentLogs)
			})
		})
	})

	// Start server
	// Start server with safe timeouts:
	// - ReadHeaderTimeout prevents Slowloris attacks by requiring request headers within 15 seconds.
	// - Global ReadTimeout and WriteTimeout are omitted to allow large authenticated source uploads
	//   and persistent WebSocket connections to stream without mid-stream socket termination.
	// - Upload body sizes are strictly guarded at the HTTP boundary via http.MaxBytesReader in SourceHandler.
	server := &http.Server{
		Addr:              cfg.Server.Addr(),
		Handler:           r,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		slog.Info("shutting down server...")

		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelShutdown()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}
	}()

	slog.Info("ForgeLab API server starting",
		"addr", cfg.Server.Addr(),
		"log_level", cfg.Log.Level,
	)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped")
}
