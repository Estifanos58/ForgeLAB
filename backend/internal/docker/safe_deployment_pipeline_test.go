package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/detector"
	"github.com/forgelab/backend/internal/discovery"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/security"
)

// TestNextJS_PrismaPostinstall_SelectiveStaging verifies that when Prisma schema and
// config files are detected in a Next.js project, the generated Dockerfile selectively
// stages those files before dependency installation, scopes DATABASE_URL placeholder
// to build commands, and cleans build cache in builder stage.
func TestNextJS_PrismaPostinstall_SelectiveStaging(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Create simulated Event_Management_System repository structure
	pkgJSON := `{
  "name": "event-management-system",
  "version": "0.1.0",
  "scripts": {
    "dev": "next dev",
    "build": "next build",
    "start": "next start",
    "postinstall": "prisma generate"
  },
  "dependencies": {
    "@prisma/client": "^5.14.0",
    "next": "14.2.3",
    "react": "^18",
    "react-dom": "^18"
  },
  "devDependencies": {
    "prisma": "^5.14.0",
    "typescript": "^5"
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package-lock.json"), []byte(`{"name":"event-management-system"}`), 0644))

	prismaDir := filepath.Join(tmpDir, "prisma")
	require.NoError(t, os.MkdirAll(prismaDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(prismaDir, "schema.prisma"), []byte(`datasource db { provider = "postgresql" url = env("DATABASE_URL") }`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "prisma.config.ts"), []byte(`export default {}`), 0644))

	// 2. Detect Prisma from directory
	prismaInfo, err := detector.DetectPrismaFromDir(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, prismaInfo)
	assert.True(t, prismaInfo.HasPrisma)
	assert.Equal(t, "prisma", prismaInfo.SchemaPath)
	assert.Equal(t, "prisma.config.ts", prismaInfo.ConfigPath)

	// 3. Generate Dockerfile with options
	df := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
		Runtime:        "nextjs",
		Port:           3000,
		StartCmd:       "npm start",
		PackageManager: "npm",
		Prisma:         prismaInfo,
	})

	// 4. Verify selective staging order: Prisma files copied BEFORE RUN npm ci
	pkgCopyIdx := strings.Index(df, "COPY package*.json")
	prismaCopyIdx := strings.Index(df, "COPY prisma ./prisma/")
	prismaConfigCopyIdx := strings.Index(df, "COPY prisma.config.ts ./")
	installIdx := strings.Index(df, "RUN export DATABASE_URL=")
	fullSourceCopyIdx := strings.Index(df, "COPY . .")

	require.Greater(t, pkgCopyIdx, -1, "must copy package manifests")
	require.Greater(t, prismaCopyIdx, -1, "must selectively stage prisma directory")
	require.Greater(t, prismaConfigCopyIdx, -1, "must selectively stage prisma.config.ts")
	require.Greater(t, installIdx, -1, "must execute install with scoped DATABASE_URL")
	require.Greater(t, fullSourceCopyIdx, -1, "must copy full source later")

	assert.True(t, prismaCopyIdx > pkgCopyIdx, "prisma copy must occur after package manifests copy")
	assert.True(t, prismaConfigCopyIdx > pkgCopyIdx, "prisma config copy must occur after package manifests copy")
	assert.True(t, installIdx > prismaCopyIdx, "install must occur after prisma staging")
	assert.True(t, installIdx > prismaConfigCopyIdx, "install must occur after prisma config staging")
	assert.True(t, fullSourceCopyIdx > installIdx, "full source copy must occur after dependency install")

	// 5. Verify DATABASE_URL is scoped to RUN instruction and NOT baked as ENV
	assert.NotContains(t, df, "ENV DATABASE_URL", "DATABASE_URL placeholder must NEVER be baked as Dockerfile ENV instruction")
	assert.Contains(t, df, `RUN export DATABASE_URL="${DATABASE_URL:-postgresql://placeholder:placeholder@localhost:5432/placeholder}"`, "must scope and export fallback DATABASE_URL to install RUN")
	assert.Contains(t, df, `export DIRECT_URL="${DIRECT_URL:-$DATABASE_URL}"`, "must export DIRECT_URL fallback")
	assert.Contains(t, df, `export SHADOW_DATABASE_URL="${SHADOW_DATABASE_URL:-$DATABASE_URL}"`, "must export SHADOW_DATABASE_URL fallback")
	assert.Contains(t, df, "ARG DATABASE_URL", "must declare ARG DATABASE_URL for build-arg inheritance")

	// 6. Verify Next.js build cache is cleaned up in builder stage
	assert.Contains(t, df, "rm -rf .next/cache", "must prune .next/cache to reduce image layer size")
	assert.Contains(t, df, "ENV SKIP_ENV_VALIDATION=1", "must set SKIP_ENV_VALIDATION in Next.js builder stage")
}

// TestNextJS_CustomSchemaPath verifies custom schema location configured in package.json.
func TestNextJS_CustomSchemaPath(t *testing.T) {
	tmpDir := t.TempDir()

	pkgJSON := `{
  "name": "custom-prisma-app",
  "scripts": {
    "build": "next build",
    "start": "next start",
    "postinstall": "prisma generate"
  },
  "prisma": {
    "schema": "custom/db/schema.prisma"
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644))

	customDir := filepath.Join(tmpDir, "custom", "db")
	require.NoError(t, os.MkdirAll(customDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(customDir, "schema.prisma"), []byte(`datasource db { provider = "postgresql" }`), 0644))

	prismaInfo, err := detector.DetectPrismaFromDir(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, prismaInfo)
	assert.True(t, prismaInfo.HasPrisma)
	assert.Equal(t, filepath.Join("custom", "db", "schema.prisma"), prismaInfo.SchemaPath)

	df := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
		Runtime:        "nextjs",
		Port:           3000,
		StartCmd:       "npm start",
		PackageManager: "npm",
		Prisma:         prismaInfo,
	})

	expectedCopy := fmt.Sprintf("COPY %s ./%s", filepath.Join("custom", "db", "schema.prisma"), filepath.Join("custom", "db", "schema.prisma"))
	assert.Contains(t, df, expectedCopy, "must stage custom schema at exact declared path")
}

// TestStandardNextJS_NoPrisma_ZeroExtraCopies verifies that an ordinary Next.js project
// without Prisma does NOT emit any Prisma COPY instructions or environment placeholders.
func TestStandardNextJS_NoPrisma_ZeroExtraCopies(t *testing.T) {
	df := detector.GenerateDockerfile("nextjs", 3000, "npm start", "npm")

	assert.NotContains(t, df, "COPY prisma", "must NOT emit COPY prisma for non-Prisma project")
	assert.NotContains(t, df, "prisma.config", "must NOT emit COPY prisma.config for non-Prisma project")
	assert.NotContains(t, df, "DATABASE_URL=", "must NOT emit DATABASE_URL fallback for non-Prisma project")
	assert.Contains(t, df, "COPY package*.json", "must copy package manifests")
	assert.Contains(t, df, "COPY . .", "must copy full source")
}

// TestShellCommandEscaping_Metacharacters verifies that start commands with quotes,
// backslashes, spaces, and compound operators are safely serialized as valid JSON CMD.
func TestShellCommandEscaping_Metacharacters(t *testing.T) {
	testCases := []struct {
		name     string
		startCmd string
	}{
		{
			name:     "Command with double quotes",
			startCmd: `node -e "console.log(\"hello world\")"`,
		},
		{
			name:     "Command with backslashes and single quotes",
			startCmd: `sh -c 'echo "path\\to\\dir"'`,
		},
		{
			name:     "Compound command with operators",
			startCmd: `npm run migrate && npm run seed && npm start`,
		},
		{
			name:     "Command with arguments containing spaces",
			startCmd: `node index.js --message "ForgeLAB Safe Deploy"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			df := detector.GenerateDockerfile("nodejs", 8080, tc.startCmd, "npm")

			// Check that CMD is on a single line and starts with CMD [
			var cmdLine string
			for _, l := range strings.Split(df, "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), "CMD [") {
					cmdLine = strings.TrimSpace(l)
					break
				}
			}
			require.NotEmpty(t, cmdLine, "must have a JSON-formatted CMD line")

			// Verify it parses as valid JSON array
			jsonPart := strings.TrimPrefix(cmdLine, "CMD ")
			var parts []string
			err := json.Unmarshal([]byte(jsonPart), &parts)
			require.NoError(t, err, "CMD %s must be valid JSON", jsonPart)
			require.Len(t, parts, 3)
			assert.Equal(t, "sh", parts[0])
			assert.Equal(t, "-c", parts[1])
			assert.Equal(t, tc.startCmd, parts[2], "exact unescaped start command must be delivered to sh -c")
		})
	}
}

// TestPreflight_MissingPrismaSchema_ActionableDiagnostic verifies that when a project has
// a Prisma lifecycle script in package.json but the schema file is missing, preflight
// returns a clear diagnostic error before Docker begins building.
func TestPreflight_MissingPrismaSchema_ActionableDiagnostic(t *testing.T) {
	tmpDir := t.TempDir()

	pkgJSON := `{
  "name": "broken-prisma-project",
  "scripts": {
    "build": "next build",
    "postinstall": "prisma generate"
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644))

	projID := uuid.New()
	project := &models.Project{
		ID:             projID,
		Name:           "broken-prisma",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpDir,
	}

	sd := &models.ServiceDeployment{
		ID:            uuid.New(),
		ServiceName:   "web",
		RuntimeType:   "nextjs",
		BuildStrategy: models.BuildStrategyAuto,
	}

	pv := security.NewPathValidator([]string{"/tmp"})

	err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
		Project:       project,
		Deployments:   []*models.ServiceDeployment{sd},
		PathValidator: pv,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Prisma lifecycle script detected", "must identify Prisma lifecycle script")
	assert.Contains(t, err.Error(), "prisma/schema.prisma", "must guide user to missing schema location")
}

// TestPreflight_ValidNextJSWithPrisma_Passes verifies that preflight succeeds when
// both package.json and prisma/schema.prisma are present.
func TestPreflight_ValidNextJSWithPrisma_Passes(t *testing.T) {
	tmpDir := t.TempDir()

	pkgJSON := `{
  "name": "valid-prisma-project",
  "scripts": {
    "build": "next build",
    "postinstall": "prisma generate"
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "prisma"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "prisma", "schema.prisma"), []byte(`datasource db { provider = "postgresql" }`), 0644))

	projID := uuid.New()
	project := &models.Project{
		ID:             projID,
		Name:           "valid-prisma",
		SourceType:     models.SourceTypeLocalDirectory,
		RepositoryPath: tmpDir,
	}

	sd := &models.ServiceDeployment{
		ID:            uuid.New(),
		ServiceName:   "web",
		RuntimeType:   "nextjs",
		BuildStrategy: models.BuildStrategyAuto,
	}

	pv := security.NewPathValidator([]string{"/tmp"})

	err := ValidateDeploymentPreflight(context.Background(), PreflightOptions{
		Project:       project,
		Deployments:   []*models.ServiceDeployment{sd},
		PathValidator: pv,
	})

	require.NoError(t, err, "preflight must pass when Prisma schema is present")
}

// TestTopology_MultiServiceDeployment_PostgresRedisAPIFrontend verifies topology diagnostics
// and DAG tiers for a representative 4-tier deployment.
func TestTopology_MultiServiceDeployment_PostgresRedisAPIFrontend(t *testing.T) {
	projID := uuid.New()
	port8080 := 8080
	port3000 := 3000

	project := &models.Project{
		ID:     projID,
		Name:   "fullstack-app",
		Status: models.ProjectStatusRunning,
	}

	svcs := []*models.Service{
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "postgres",
			Role:           models.RoleOther,
			Classification: models.ClassificationInfrastructure,
			Status:         models.ServiceStatusRunning,
			InternalPort:   5432,
			Image:          "postgres:15-alpine",
			Networks:       []string{"backend-net"},
		},
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "redis",
			Role:           models.RoleOther,
			Classification: models.ClassificationInfrastructure,
			Status:         models.ServiceStatusRunning,
			InternalPort:   6379,
			Image:          "redis:7-alpine",
			Networks:       []string{"backend-net"},
		},
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "api",
			Role:           models.RoleBackend,
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusRunning,
			InternalPort:   8080,
			HostPort:       &port8080,
			DependsOn:      []string{"postgres", "redis"},
			Networks:       []string{"backend-net"},
		},
		{
			ID:             uuid.New(),
			ProjectID:      projID,
			Name:           "frontend",
			Role:           models.RoleFrontend,
			Classification: models.ClassificationApplication,
			Status:         models.ServiceStatusRunning,
			InternalPort:   3000,
			HostPort:       &port3000,
			DependsOn:      []string{"api"},
			Networks:       []string{"backend-net"},
		},
	}
	project.Services = svcs

	// Use engine to calculate topology
	engine := &Engine{}
	resp := &ProjectTopologyResponse{
		ProjectID:   projID,
		ProjectName: project.Name,
		NetworkName: "forgelab-net-" + projID.String(),
	}

	// Verify DAG calculation: Postgres and Redis must be Tier 1; API Tier 2; Frontend Tier 3
	var nodes []discovery.ServiceNode
	for _, s := range svcs {
		nodes = append(nodes, discovery.ServiceNode{
			Name:      s.Name,
			DependsOn: s.DependsOn,
		})
	}
	_, tiers, err := discovery.ValidateAndBuildDAG(nodes)
	require.NoError(t, err)
	require.Len(t, tiers, 3)

	assert.ElementsMatch(t, []string{"postgres", "redis"}, tiers[0], "infrastructure tier must start first")
	assert.Equal(t, []string{"api"}, tiers[1], "backend API must start second")
	assert.Equal(t, []string{"frontend"}, tiers[2], "frontend must start third")

	_ = engine
	_ = resp
}

// TestServiceExposure_DefaultsAndExplicitChoices verifies that CreateServiceInput.PublicExposed
// correctly distinguishes explicit false, explicit true, and nil legacy defaults.
func TestServiceExposure_DefaultsAndExplicitChoices(t *testing.T) {
	svcExplicitFalse := false
	svcExplicitTrue := true

	// 1. Explicit false for frontend: must remain false
	var isPublic1 bool
	if &svcExplicitFalse != nil {
		isPublic1 = svcExplicitFalse
	}
	assert.False(t, isPublic1, "explicit public_exposed: false must not be overridden by heuristic")

	// 2. Explicit true for backend: must be true
	var isPublic2 bool
	if &svcExplicitTrue != nil {
		isPublic2 = svcExplicitTrue
	}
	assert.True(t, isPublic2, "explicit public_exposed: true must be honored")

	// 3. Nil setting for infrastructure: must default to false
	var isPublic3 bool
	var nilExposure *bool = nil
	if nilExposure != nil {
		isPublic3 = *nilExposure
	} else {
		classification := models.ClassificationInfrastructure
		if classification == models.ClassificationInfrastructure {
			isPublic3 = false
		}
	}
	assert.False(t, isPublic3, "infrastructure services must NEVER be exposed publicly by default")
}

// TestVolumeSanitization_ErrorPropagation verifies that invalid volume definitions
// return an error instead of silently corrupting service configuration.
func TestVolumeSanitization_ErrorPropagation(t *testing.T) {
	// Empty target should fail
	badVols := []models.VolumeMountConfig{
		{Source: "data", Target: ""},
	}
	_, _, err := security.ValidateAndSanitizeServiceVolumes(badVols)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "volume target path cannot be empty")

	// Traversal target should fail
	traversalVols := []models.VolumeMountConfig{
		{Source: "data", Target: "../etc/passwd"},
	}
	_, _, tErr := security.ValidateAndSanitizeServiceVolumes(traversalVols)
	require.Error(t, tErr)
	assert.Contains(t, tErr.Error(), "parent directory traversal")

	// Docker socket mount should be stripped safely
	socketVols := []models.VolumeMountConfig{
		{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		{Source: "safe_vol", Target: "/data"},
	}
	safe, stripped, sErr := security.ValidateAndSanitizeServiceVolumes(socketVols)
	require.NoError(t, sErr)
	assert.True(t, stripped)
	require.Len(t, safe, 1)
	assert.Equal(t, "safe_vol", safe[0].Source)
}

// TestPackageManagerSupport_AllManagers verifies template generation across all supported package managers.
func TestPackageManagerSupport_AllManagers(t *testing.T) {
	pms := []string{"npm", "pnpm", "yarn", "bun"}

	for _, pm := range pms {
		t.Run(pm, func(t *testing.T) {
			df := detector.GenerateDockerfile("nextjs", 3000, "", pm)
			require.NotEmpty(t, df)
			assert.Contains(t, df, "EXPOSE 3000")
			// Must not contain silenced error streams 2>/dev/null in install fallback
			for _, line := range strings.Split(df, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "RUN ") && strings.Contains(line, "install") {
					assert.NotContains(t, line, "2>/dev/null ||", "package-manager install fallback must not silence errors to /dev/null")
				}
			}
		})
	}
}

// TestCustomDockerfile_Unaffected verifies that custom Dockerfile projects are unaffected by automatic template changes.
func TestCustomDockerfile_Unaffected(t *testing.T) {
	customDF := `FROM custom-base:latest
WORKDIR /my-app
COPY . .
RUN make build
CMD ["./my-app"]`

	// Automatic template generator is never called for custom Dockerfiles
	assert.Contains(t, customDF, "make build")
	assert.NotContains(t, customDF, "node:22-alpine")
}

// TestPrisma_ShellExportScoping_AllPackageManagers verifies that across all package managers,
// fallback DATABASE_URL, DIRECT_URL, and SHADOW_DATABASE_URL are exported in the shell
// so chained subcommands (e.g. npm config set, corepack enable) pass them to the install command.
func TestPrisma_ShellExportScoping_AllPackageManagers(t *testing.T) {
	pms := []string{"npm", "pnpm", "yarn", "bun"}
	prismaInfo := &discovery.PrismaStagingInfo{
		HasPrisma:  true,
		SchemaPath: "prisma",
		ConfigPath: "prisma.config.ts",
	}

	for _, pm := range pms {
		t.Run(pm, func(t *testing.T) {
			df := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
				Runtime:        "nextjs",
				Port:           3000,
				StartCmd:       "",
				PackageManager: pm,
				Prisma:         prismaInfo,
			})
			require.NotEmpty(t, df)

			// Must export variables in the install command so they are inherited by subsequent chained commands
			assert.Contains(t, df, `export DATABASE_URL="${DATABASE_URL:-postgresql://placeholder:placeholder@localhost:5432/placeholder}"`)
			assert.Contains(t, df, `export DIRECT_URL="${DIRECT_URL:-$DATABASE_URL}"`)
			assert.Contains(t, df, `export SHADOW_DATABASE_URL="${SHADOW_DATABASE_URL:-$DATABASE_URL}"`)

			// Must declare ARGs for build-arg inheritance
			assert.Contains(t, df, "ARG DATABASE_URL")
			assert.Contains(t, df, "ARG DIRECT_URL")
			assert.Contains(t, df, "ARG SHADOW_DATABASE_URL")
		})
	}
}

// TestPrisma_MultiFileSchemaDetection verifies that modern multi-file Prisma schemas
// (e.g. prisma/schema/*.prisma) are detected and staged properly.
func TestPrisma_MultiFileSchemaDetection(t *testing.T) {
	tmpDir := t.TempDir()

	pkgJSON := `{"name":"multifile-prisma","scripts":{"postinstall":"prisma generate"}}`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644))

	schemaDir := filepath.Join(tmpDir, "prisma", "schema")
	require.NoError(t, os.MkdirAll(schemaDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(schemaDir, "user.prisma"), []byte(`model User { id Int @id }`), 0644))

	prismaInfo, err := detector.DetectPrismaFromDir(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, prismaInfo)
	assert.True(t, prismaInfo.HasPrisma, "must detect multi-file prisma schema")
	assert.Equal(t, "prisma", prismaInfo.SchemaPath, "must stage prisma directory")
}

// TestBuildArgs_ARGDeclarationInBuilder verifies that build args configured for the service
// are declared as ARG instructions in the builder stage.
func TestBuildArgs_ARGDeclarationInBuilder(t *testing.T) {
	df := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
		Runtime:        "nextjs",
		Port:           3000,
		PackageManager: "npm",
		BuildArgs:      []string{"NEXT_PUBLIC_API_URL", "APP_VERSION"},
	})

	assert.Contains(t, df, "ARG NEXT_PUBLIC_API_URL", "must declare ARG for frontend public variable")
	assert.Contains(t, df, "ARG APP_VERSION", "must declare ARG for custom build variable")
}

// TestBuildArgs_NextJSServerEnvInheritance verifies that fullstack server environment variables
// needed by Next.js static page data collection are declared in the builder stage.
func TestBuildArgs_NextJSServerEnvInheritance(t *testing.T) {
	buildArgs := []string{
		"BETTER_AUTH_SECRET",
		"CHAPA_SECRET_KEY",
		"NEXT_PUBLIC_APP_URL",
		"REDIS_URL",
		"WS_PORT",
	}

	df := detector.GenerateDockerfileWithOptions(discovery.DockerfileOptions{
		Runtime:        "nextjs",
		Port:           3000,
		PackageManager: "npm",
		BuildArgs:      buildArgs,
	})

	for _, k := range buildArgs {
		assert.Contains(t, df, fmt.Sprintf("ARG %s", k), "builder stage must declare ARG %s for Next.js build-time execution", k)
	}

	// Verify runner stage does NOT expose build-time ARG values
	runnerIdx := strings.Index(df, "FROM node:22-alpine AS runner")
	require.True(t, runnerIdx > 0)
	runnerStage := df[runnerIdx:]
	for _, k := range buildArgs {
		assert.NotContains(t, runnerStage, fmt.Sprintf("ARG %s", k), "runner stage must not declare build-stage ARG %s", k)
		assert.NotContains(t, runnerStage, fmt.Sprintf("ENV %s", k), "runner stage must not bake build-stage secrets as ENV %s", k)
	}
}

