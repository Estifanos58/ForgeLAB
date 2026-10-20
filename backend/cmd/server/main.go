package main

import (
	"context"
	"flag"
	"fmt"
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
	"github.com/forgelab/backend/internal/ratelimit"
	"github.com/forgelab/backend/internal/security"
	"github.com/forgelab/backend/internal/services"
	"github.com/forgelab/backend/internal/tunnel"
	ws "github.com/forgelab/backend/internal/websocket"
)

var (
	Version   = "1.0.0"
	CommitSHA = "dev"
	BuildTime = "unknown"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Printf("forgelab-server v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
		return
	}

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

	fmt.Printf("====================================================\n")
	fmt.Printf("   ForgeLAB Backend Server v%s (commit: %s, built: %s)\n", Version, CommitSHA, BuildTime)
	fmt.Printf("   Listening on: http://%s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("   Environment:  %s\n", cfg.App.Environment)
	fmt.Printf("====================================================\n")
	slog.Info("starting ForgeLAB backend server", "version", Version, "commit", CommitSHA, "build_time", BuildTime, "env", cfg.App.Environment)

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

	// Auto-detect host source root from Docker container mount metadata if running in container and not explicitly configured
	var daemonSourceRoot string
	if cfg.Docker.HostSourceRoot == "" && dockerCli != nil {
		if mountInfo, err := docker.DetectBackendContainerMount(ctx, dockerCli, cfg.Docker.ContainerSourceRoot); err == nil && mountInfo != nil {
			slog.Info("auto-detected host source root from Docker container mount metadata",
				"daemon_source_root", mountInfo.DaemonSource,
				"container_source_root", mountInfo.ContainerTarget,
			)
			daemonSourceRoot = mountInfo.DaemonSource
			cfg.Docker.HostSourceRoot = docker.DaemonPathToHostPath(mountInfo.DaemonSource)
		}
	} else if cfg.Docker.HostSourceRoot != "" {
		daemonSourceRoot = docker.HostPathToDaemonPath(cfg.Docker.HostSourceRoot, "")
	}

	pathValidator := security.NewPathValidatorWithMapping(
		allowedRoots,
		cfg.Docker.HostSourceRoot,
		cfg.Docker.ContainerSourceRoot,
	)
	if daemonSourceRoot != "" {
		pathValidator.SetDaemonSourceRoot(daemonSourceRoot)
	}
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
	githubAppManager := services.NewGitHubAppManager(cfg.GitHubApp, pool, nil)
	githubService.SetGitHubAppManager(githubAppManager)
	repositoryImportService := services.NewRepositoryImportService(pool, cfg.Docker.SourcesDir, githubService)

	userService := services.NewUserService(pool, jwtManager)
	if cfg.App.AllowGuestMode {
		if err := userService.EnsureGuestUser(ctx); err != nil {
			slog.Error("failed to ensure guest user identity during startup", "error", err)
			os.Exit(1)
		}
		middleware.SetGuestEnsurer(userService.EnsureGuestUserWithID)
		slog.Info("ensured shared guest user identity in database", "guest_user_id", models.GuestUserID)
	}
	serviceService := services.NewServiceService(pool)
	projectService := services.NewProjectService(pool, pathValidator, sourceService, githubService)
	projectService.SetServiceService(serviceService)
	projectService.SetImportService(repositoryImportService)
	deploymentService := services.NewDeploymentService(pool)
	secretService := services.NewSecretService(pool, encryptor, projectService)
	deploymentService.SetSecretService(secretService)
	deploymentService.SetGitHubService(githubService)
	projectService.SetSecretService(secretService)
	projectService.SetDeploymentService(deploymentService)

	// Public Project Sharing Service
	shareService := services.NewShareService(
		pool,
		projectService,
		serviceService,
		cfg.Share.Enabled,
		cfg.Share.GatewayPublicURL,
		cfg.Share.GatewayTunnelURL,
	)

	// Initialize WebSocket Hub
	wsHub := ws.NewHub(jwtManager, projectService, deploymentService, redisClient, cfg.App.AllowedOriginsList()...)
	wsHub.SetServiceDeploymentResolver(deploymentService)
	wsHub.SetServiceResolver(serviceService)
	wsHub.SetAllowGuestMode(cfg.App.AllowGuestMode)

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
	dockerEngine.SetPublicHost(cfg.App.PublicHostName())
	if cfg.Share.AppHostIP != "" {
		dockerEngine.SetAppHostIP(cfg.Share.AppHostIP)
	}
	if cfg.Docker.PruneFilterLabel != "" {
		dockerEngine.SetPruneFilterLabel(cfg.Docker.PruneFilterLabel)
	}
	defer dockerEngine.StopAllLogCollectors()

	// Initialize Public Sharing Tunnel & Local Router
	dockerUpstreamResolver := tunnel.NewDockerUpstreamResolver(dockerCli)
	localRouter := tunnel.NewLocalRouter(shareService, serviceService, dockerUpstreamResolver)
	tunnelManager := tunnel.NewTunnelManager(tunnel.TunnelManagerConfig{
		Enabled:          cfg.Share.Enabled,
		GatewayTunnelURL: cfg.Share.GatewayTunnelURL,
		GatewayAuthToken: cfg.Share.GatewayAuthToken,
		TLSSkipVerify:    cfg.Share.TLSSkipVerify,
		InstallationID:   cfg.Server.Host,
	}, localRouter, shareService)
	shareService.SetTunnelRegistrar(tunnelManager)
	tunnelManager.Start(ctx)
	defer tunnelManager.Stop()

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
				if models.IsDeploymentTerminalStatus(sd.Status) {
					return true, nil
				}
				if superseded, _ := deploymentService.IsServiceDeploymentSuperseded(ctx, sd.ServiceID, sd.DeployNumber); superseded {
					return true, nil
				}
				return false, nil
			}
			dep, err := deploymentService.GetDeployment(ctx, job.ID)
			if err != nil {
				return false, err
			}
			if models.IsDeploymentTerminalStatus(dep.Status) {
				return true, nil
			}
			if superseded, _ := deploymentService.IsDeploymentSuperseded(ctx, dep.ProjectID, dep.DeployNumber); superseded {
				return true, nil
			}
			return false, nil
		})
		defer deployQueue.Stop()
	}

	// Startup reconciliation for Docker containers and orphaned / missing deployments
	reconcileFn := func() {
		// Reconcile Docker containers and host port bindings
		if err := dockerEngine.ReconcileDaemonContainers(ctx); err != nil {
			slog.Warn("failed to reconcile daemon containers", "error", err)
		}

		// Perform continuous runtime health inspection to detect OOM kills and crash loops
		if err := dockerEngine.CheckContinuousRuntimeHealth(ctx); err != nil {
			slog.Warn("failed to inspect continuous runtime container health", "error", err)
		}

		if deployQueue != nil {
			// Periodic recovery of abandoned/expired Redis queue jobs
			if recCount, err := deployQueue.RecoverAbandonedJobs(ctx); err != nil {
				slog.Warn("failed to recover abandoned queue jobs during reconciliation", "error", err)
			} else if recCount > 0 {
				slog.Info("reconciled abandoned queue jobs", "count", recCount)
			}

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

	// Run initial startup reconciliation synchronously before accepting HTTP traffic and starting queue workers
	// to fix startup races where workers or dashboard requests could see stale states.
	slog.Info("running initial container and runtime status reconciliation...")
	startupReconcileCtx, startupCancel := context.WithTimeout(ctx, 15*time.Second)
	if err := dockerEngine.ReconcileDaemonContainers(startupReconcileCtx); err != nil {
		slog.Warn("initial daemon container reconciliation failed", "error", err)
	}
	if err := dockerEngine.CheckContinuousRuntimeHealth(startupReconcileCtx); err != nil {
		slog.Warn("initial runtime container health check failed", "error", err)
	}
	startupCancel()
	slog.Info("initial container and runtime status reconciliation complete")

	// Start deployment queue workers AFTER initial Docker and runtime reconciliation completes
	if deployQueue != nil {
		deployQueue.StartJobWorker(ctx, func(workerCtx context.Context, job queue.Job) error {
			if job.Type == queue.JobTypeServiceDeployment {
				return dockerEngine.ExecuteServiceDeployment(workerCtx, job.ID)
			}
			return dockerEngine.ExecuteDeployment(workerCtx, job.ID)
		})
	}

	// Launch periodic background reconciliation for continuous health inspection
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

	// Launch periodic garbage collection on a separate, less frequent schedule
	// Protected by the exclusive build-maintenance lock inside dockerEngine to prevent overlap with builds
	pruneInterval := cfg.Docker.PruneInterval
	if pruneInterval <= 0 {
		pruneInterval = 1 * time.Hour
	}
	go func() {
		pruneTicker := time.NewTicker(pruneInterval)
		defer pruneTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pruneTicker.C:
				if err := dockerEngine.PruneDanglingResources(ctx, 2*time.Hour); err != nil {
					slog.Warn("failed to prune dangling build resources", "error", err)
				}
			}
		}
	}()

	// Launch periodic log retention cleanup
	logCleanupInterval := cfg.Log.CleanupInterval
	if logCleanupInterval <= 0 {
		logCleanupInterval = 1 * time.Hour
	}
	if cfg.Log.RetentionDays > 0 || cfg.Log.MaxLogsPerDeployment > 0 {
		go func() {
			logTicker := time.NewTicker(logCleanupInterval)
			defer logTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-logTicker.C:
					pruned, err := deploymentService.PruneLogs(ctx, cfg.Log.RetentionDays, cfg.Log.MaxLogsPerDeployment)
					if err != nil {
						slog.Warn("failed to prune deployment logs", "error", err)
					} else if pruned > 0 {
						slog.Info("pruned expired/excess deployment logs", "count", pruned)
					}
				}
			}
		}()
	}

	// Launch periodic expired repository imports cleanup
	go func() {
		importTicker := time.NewTicker(30 * time.Minute)
		defer importTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-importTicker.C:
				repositoryImportService.PruneExpiredImports(ctx)
			}
		}
	}()

	// Rate Limiting (Fixed-Window Counter with Trusted Proxy validation)
	rateLimiter := ratelimit.NewLimiter(redisClient, cfg.RateLimit.Enabled, cfg.RateLimit.TrustedProxies...)
	wsHub.SetRateLimiter(rateLimiter, cfg.RateLimit.WSSubscriptionLimit)

	// Initialize handlers
	oauthService := services.NewOAuthService(cfg.Google, cfg.GitHub, redisClient)
	authHandler := handlers.NewAuthHandler(userService, oauthService, cfg.App.FrontendURL, cfg.App.CookieSecure)
	authHandler.SetAllowGuestMode(cfg.App.AllowGuestMode)
	projectHandler := handlers.NewProjectHandler(projectService, deploymentService, dockerEngine, deployQueue)
	serviceHandler := handlers.NewServiceHandler(serviceService, projectService, deploymentService, dockerEngine, deployQueue)
	envHandler := handlers.NewEnvHandler(secretService, projectService)
	integrationHandler := handlers.NewIntegrationHandler(githubService, githubAppManager, repositoryImportService, cfg.App.FrontendURL, cfg.App.CookieSecure)
	sourceHandler := handlers.NewSourceHandler(sourceService, pathValidator)
	discoveryHandler := handlers.NewDiscoveryHandler(githubService, sourceService, pathValidator, secretService)
	shareHandler := handlers.NewShareHandler(shareService, projectService)
	healthHandler := handlers.NewHealthHandler(pool, redisClient, dockerCli)
	healthHandler.SetSecurityContext(jwtManager, cfg.RateLimit.TrustedProxies)
	healthHandler.SetQueueDiagnosticsProvider(deployQueue)
	healthHandler.SetBuildQueueDiagnosticsProvider(dockerEngine)
	healthHandler.SetLogCollectorDiagnosticsProvider(dockerEngine)
	healthHandler.SetReconciliationProvider(dockerEngine)

	// Setup router
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(middleware.RequestLogger)
	r.Use(middleware.MaxBodySize(10*1024*1024, 105*1024*1024))
	r.Use(middleware.CORS(cfg.App.AllowedOriginsList()...))
	r.Use(chimiddleware.Recoverer)

	// Health and readiness checks (unauthenticated)
	r.Get("/health", healthHandler.Liveness)
	r.Get("/ready", healthHandler.Readiness)
	r.Get("/health/ready", healthHandler.Readiness)
	r.Get("/health/queue", healthHandler.QueueDiagnostics)
	r.Get("/health/logs", healthHandler.LogCollectorDiagnostics)

	// Throttles for sensitive endpoints
	authThrottle := ratelimit.Middleware(rateLimiter, "auth", cfg.RateLimit.AuthLimit, time.Minute)
	sourceThrottle := ratelimit.Middleware(rateLimiter, "source", cfg.RateLimit.SourceLimit, time.Minute)
	deployThrottle := ratelimit.Middleware(rateLimiter, "deploy", cfg.RateLimit.DeployLimit, time.Minute)
	wsConnThrottle := ratelimit.Middleware(rateLimiter, "ws_conn", cfg.RateLimit.WSConnLimit, time.Minute)

	// WebSocket endpoint & ticket generation
	r.With(wsConnThrottle).Get("/api/ws", wsHub.ServeWS)
	r.With(wsConnThrottle).Post("/api/ws/ticket", wsHub.CreateTicketHandler)

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.ContentTypeJSON)

		// Public GitHub OAuth & GitHub App integration callbacks
		r.Get("/integrations/github/callback", integrationHandler.GitHubCallback)
		r.Get("/integrations/github/app/callback", integrationHandler.GitHubAppCallback)

		// Agent session validation (token-authenticated by agent)
		r.Post("/sources/agent/session/validate", sourceHandler.ValidateAgentSession)

		// Auth routes (public)
		r.Route("/auth", func(r chi.Router) {
			r.Get("/mode", authHandler.Mode)
			r.With(authThrottle).Post("/register", authHandler.Register)
			r.With(authThrottle).Post("/login", authHandler.Login)
			r.With(authThrottle).Post("/refresh", authHandler.Refresh)
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

		// GitHub Integrations & Imports (accessible to registered and guest users; permissions enforced per-repo)
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthOrGuestMiddleware(jwtManager, cfg.App.AllowGuestMode, cfg.App.AllowedOriginsList()...))

			// Integrations (GitHub Repository Access)
			r.Route("/integrations", func(r chi.Router) {
				r.Route("/github", func(r chi.Router) {
					r.Get("/", integrationHandler.GetGitHubStatus)
					r.Get("/connect", integrationHandler.ConnectGitHub)
					r.Post("/connect", integrationHandler.ConnectGitHub)
					r.Post("/token", integrationHandler.ConnectGitHubPAT)
					r.Get("/app/install", integrationHandler.GetAppInstallURL)
					r.Get("/orgs", integrationHandler.ListOrganizations)
					r.Post("/disconnect", integrationHandler.DisconnectGitHub)
					r.Get("/repositories", integrationHandler.ListRepositories)
					r.Get("/repositories/{owner}/{repo}", integrationHandler.GetRepository)
					r.Get("/repositories/{owner}/{repo}/branches", integrationHandler.ListBranches)
					r.Get("/repositories/{owner}/{repo}/detect", integrationHandler.DetectRepository)
					r.Post("/repositories/{owner}/{repo}/detect", integrationHandler.DetectRepository)
					r.Post("/imports", integrationHandler.StartImport)
					r.Get("/imports/{id}", integrationHandler.GetImport)
					r.Post("/imports/{id}/cancel", integrationHandler.CancelImport)
					r.Post("/imports/{id}/retry", integrationHandler.RetryImport)
				})
			})
		})

		// Protected or Guest routes (Local Deployments)
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthOrGuestMiddleware(jwtManager, cfg.App.AllowGuestMode, cfg.App.AllowedOriginsList()...))

			// Source Management & Uploads
			r.Route("/sources", func(r chi.Router) {
				r.With(sourceThrottle).Post("/upload", sourceHandler.Upload)
				r.Post("/agent/session", sourceHandler.CreateAgentSession)
				r.Post("/agent/session/revoke", sourceHandler.RevokeAgentSession)
				r.Post("/agent/register", sourceHandler.RegisterAgentSource)
				r.With(sourceThrottle).Post("/local/validate", sourceHandler.ValidateLocalPath)
				r.Get("/local/validate/{sessionId}", sourceHandler.GetLocalValidationStatus)
				r.Get("/{id}", sourceHandler.GetStatus)
				r.Delete("/{id}", sourceHandler.Delete)
			})

			// Discovery & Deployment Plan
			r.Route("/discovery", func(r chi.Router) {
				r.With(sourceThrottle).Post("/plan", discoveryHandler.GeneratePlan)
			})

			// Projects
			r.Route("/projects", func(r chi.Router) {
				r.Post("/", projectHandler.Create)
				r.Get("/", projectHandler.List)
				r.Get("/{id}", projectHandler.Get)
				r.Get("/{id}/topology", projectHandler.GetTopology)
				r.Patch("/{id}", projectHandler.Update)
				r.Delete("/{id}", projectHandler.Delete)

				// Service Lifecycle Controls & Listing
				r.Get("/{id}/services", serviceHandler.List)
				r.With(deployThrottle).Post("/{id}/services/{serviceId}/deploy", serviceHandler.Deploy)
				r.With(deployThrottle).Post("/{id}/services/{serviceId}/rollback", serviceHandler.Rollback)
				r.Get("/{id}/services/{serviceId}/deployments", serviceHandler.ListDeployments)
				r.Get("/{id}/services/{serviceId}/deployments/{deploymentId}", serviceHandler.GetDeployment)
				r.Post("/{id}/services/{serviceId}/deployments/{deploymentId}/cancel", serviceHandler.CancelDeployment)
				r.Get("/{id}/services/{serviceId}/deployments/{deploymentId}/logs", serviceHandler.GetLogs)
				r.Post("/{id}/services/{serviceId}/stop", serviceHandler.Stop)
				r.Post("/{id}/services/{serviceId}/start", serviceHandler.Start)
				r.Post("/{id}/services/{serviceId}/restart", serviceHandler.Restart)
				r.Patch("/{id}/services/{serviceId}/resources", serviceHandler.UpdateResources)

				// Application Lifecycle Controls
				r.Post("/{id}/stop", projectHandler.Stop)
				r.Post("/{id}/start", projectHandler.Start)
				r.Post("/{id}/restart", projectHandler.Restart)
				r.With(deployThrottle).Post("/{id}/rollback", projectHandler.Rollback)

				// Environment Variables / Secrets
				r.Get("/{id}/env", envHandler.List)
				r.Post("/{id}/env", envHandler.Set)
				r.Post("/{id}/env/import", envHandler.ImportLocalEnv)
				r.Delete("/{id}/env/{key}", envHandler.Delete)

				// Project-level logs
				r.Get("/{id}/logs", projectHandler.GetProjectLogs)

				// Public Sharing
				r.Get("/{id}/share", shareHandler.Get)
				r.Post("/{id}/share", shareHandler.Create)
				r.Delete("/{id}/share", shareHandler.Revoke)

				// Deployments
				r.With(deployThrottle).Post("/{id}/deployments", projectHandler.Deploy)
				r.Get("/{id}/deployments", projectHandler.ListDeployments)
				r.Get("/{id}/deployments/{deploymentId}", projectHandler.GetDeployment)
				r.Post("/{id}/deployments/{deploymentId}/cancel", projectHandler.CancelDeployment)
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
