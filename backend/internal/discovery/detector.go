package discovery

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/forgelab/backend/internal/models"
)

var exposeRegex = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(\d+)`)

// DetectTechnology inspects a set of files and determines the runtime, framework, port, and start/build commands
func DetectTechnology(files map[string][]byte) (runtime, framework, pkgManager string, port int, healthPath, healthStrat, buildCmd, startCmd string) {
	// Defaults
	runtime = "generic"
	framework = "Generic Application"
	pkgManager = ""
	port = 8080
	healthPath = "/health"
	healthStrat = models.HealthStrategyAuto
	buildCmd = ""
	startCmd = ""

	// 1. Dockerfile check
	for f, content := range files {
		if strings.EqualFold(filepath.Base(f), "dockerfile") {
			for _, line := range strings.Split(string(content), "\n") {
				if match := exposeRegex.FindStringSubmatch(strings.TrimSpace(line)); len(match) > 1 {
					if p, err := strconv.Atoi(match[1]); err == nil && p > 0 && p <= 65535 {
						port = p
						break
					}
				}
			}
		}
	}

	// 2. Node.js Ecosystem
	if pkgRaw, ok := files["package.json"]; ok {
		runtime = "nodejs"
		framework = "Node.js"
		port = 3000
		healthPath = "/"
		healthStrat = models.HealthStrategyHTTP

		if _, ok := files["pnpm-lock.yaml"]; ok {
			pkgManager = "pnpm"
		} else if _, ok := files["yarn.lock"]; ok {
			pkgManager = "yarn"
		} else if _, ok := files["bun.lockb"]; ok || hasKey(files, "bun.lock") {
			pkgManager = "bun"
		} else {
			pkgManager = "npm"
		}

		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
			Main            string            `json:"main"`
		}
		_ = json.Unmarshal(pkgRaw, &pkg)

		allDeps := make(map[string]string)
		for k, v := range pkg.Dependencies {
			allDeps[k] = v
		}
		for k, v := range pkg.DevDependencies {
			allDeps[k] = v
		}

		// Next.js
		if hasDep(allDeps, "next") || hasFileWithPrefix(files, "next.config.") {
			runtime = "nextjs"
			framework = "Next.js"
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = formatRunCommand(pkgManager, "start")
			return
		}

		// Vite / React / Vue / Svelte
		if hasDep(allDeps, "vite") || hasFileWithPrefix(files, "vite.config.") {
			if hasDep(allDeps, "vue") {
				runtime = "vue-vite"
				framework = "Vue (Vite)"
			} else if hasDep(allDeps, "svelte") {
				runtime = "svelte-vite"
				framework = "Svelte (Vite)"
			} else {
				runtime = "react-vite"
				framework = "React (Vite)"
			}
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = fmt.Sprintf("npx serve -s dist -l %d", port)
			return
		}

		// Nuxt
		if hasDep(allDeps, "nuxt") || hasFileWithPrefix(files, "nuxt.config.") {
			runtime = "nuxtjs"
			framework = "Nuxt"
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = formatRunCommand(pkgManager, "start")
			return
		}

		// NestJS
		if hasDep(allDeps, "@nestjs/core") || hasKey(files, "nest-cli.json") {
			runtime = "nestjs"
			framework = "NestJS"
			port = 3000
			healthPath = "/"
			buildCmd = formatRunCommand(pkgManager, "build")
			startCmd = formatRunCommand(pkgManager, "start:prod")
			return
		}

		// Express / Fastify / Koa
		if hasDep(allDeps, "express") {
			runtime = "express"
			framework = "Express"
			port = 3000
		} else if hasDep(allDeps, "fastify") {
			runtime = "fastify"
			framework = "Fastify"
			port = 3000
		}

		if _, ok := pkg.Scripts["build"]; ok {
			buildCmd = formatRunCommand(pkgManager, "build")
		}
		if _, ok := pkg.Scripts["start"]; ok {
			startCmd = formatRunCommand(pkgManager, "start")
		} else if pkg.Main != "" {
			startCmd = "node " + pkg.Main
		} else {
			startCmd = "node index.js"
		}
		return
	}

	// 3. Python Ecosystem
	hasPyReqs := false
	var pyContent string
	if c, ok := files["requirements.txt"]; ok {
		hasPyReqs = true
		pyContent = string(c)
		pkgManager = "pip"
	} else if c, ok := files["pyproject.toml"]; ok {
		hasPyReqs = true
		pyContent = string(c)
		if hasKey(files, "uv.lock") {
			pkgManager = "uv"
		} else if hasKey(files, "poetry.lock") {
			pkgManager = "poetry"
		} else {
			pkgManager = "pip"
		}
	} else if c, ok := files["Pipfile"]; ok {
		hasPyReqs = true
		pyContent = string(c)
		pkgManager = "pipenv"
	}

	if hasPyReqs || hasKey(files, "main.py") || hasKey(files, "app.py") || hasKey(files, "manage.py") || hasKey(files, "wsgi.py") {
		lowerPy := strings.ToLower(pyContent)
		if strings.Contains(lowerPy, "fastapi") || strings.Contains(lowerPy, "uvicorn") {
			runtime = "python-fastapi"
			framework = "FastAPI"
			port = 8000
			healthPath = "/docs"
			healthStrat = models.HealthStrategyHTTP
			startCmd = "uvicorn main:app --host 0.0.0.0 --port 8000"
			return
		}
		if strings.Contains(lowerPy, "flask") {
			runtime = "python-flask"
			framework = "Flask"
			port = 5000
			healthPath = "/"
			healthStrat = models.HealthStrategyHTTP
			startCmd = "gunicorn --bind 0.0.0.0:5000 --workers 2 app:app"
			return
		}
		if strings.Contains(lowerPy, "django") || hasKey(files, "manage.py") {
			runtime = "python-django"
			framework = "Django"
			port = 8000
			healthPath = "/"
			healthStrat = models.HealthStrategyHTTP
			startCmd = "gunicorn --bind 0.0.0.0:8000 --workers 2 wsgi:application"
			return
		}

		runtime = "python"
		framework = "Python"
		port = 8000
		healthPath = "/"
		healthStrat = models.HealthStrategyHTTP
		if hasKey(files, "app.py") {
			startCmd = "python app.py"
		} else {
			startCmd = "python main.py"
		}
		return
	}

	// 4. PHP / Laravel
	if hasKey(files, "composer.json") || hasKey(files, "artisan") || hasKey(files, "index.php") {
		runtime = "php"
		pkgManager = "composer"
		port = 8000
		healthPath = "/"
		healthStrat = models.HealthStrategyHTTP
		if hasKey(files, "artisan") {
			framework = "Laravel"
			startCmd = "php -d variables_order=EGPCS -S 0.0.0.0:8000 -t public"
		} else {
			framework = "PHP"
			startCmd = "php -S 0.0.0.0:8000"
		}
		return
	}

	// 5. Go Ecosystem
	if modRaw, ok := files["go.mod"]; ok {
		runtime = "go"
		framework = "Go"
		pkgManager = "go modules"
		port = 8080
		healthPath = "/health"
		healthStrat = models.HealthStrategyHTTP
		buildCmd = "go build -o /app/server ."
		startCmd = "/app/server"

		lowerMod := strings.ToLower(string(modRaw))
		if strings.Contains(lowerMod, "gin-gonic/gin") {
			framework = "Go (Gin)"
		} else if strings.Contains(lowerMod, "gofiber/fiber") {
			framework = "Go (Fiber)"
		} else if strings.Contains(lowerMod, "labstack/echo") {
			framework = "Go (Echo)"
		} else if strings.Contains(lowerMod, "go-chi/chi") {
			framework = "Go (Chi)"
		}
		return
	}

	// 6. Java Ecosystem
	if pomRaw, ok := files["pom.xml"]; ok {
		pkgManager = "maven"
		port = 8080
		healthPath = "/health"
		healthStrat = models.HealthStrategyHTTP
		buildCmd = "mvn clean package -DskipTests"
		startCmd = "java -jar /app/app.jar"

		if strings.Contains(string(pomRaw), "spring-boot") {
			runtime = "java"
			framework = "Spring Boot (Maven)"
			healthPath = "/actuator/health"
		} else {
			runtime = "java"
			framework = "Java (Maven)"
		}
		return
	}

	if hasFileWithExt(files, ".gradle") || hasFileWithExt(files, ".gradle.kts") {
		pkgManager = "gradle"
		port = 8080
		healthPath = "/health"
		healthStrat = models.HealthStrategyHTTP
		buildCmd = "./gradlew build -x test"
		startCmd = "java -jar /app/app.jar"

		isSpring := false
		for _, content := range files {
			lowerContent := strings.ToLower(string(content))
			if strings.Contains(lowerContent, "spring-boot") || strings.Contains(lowerContent, "springframework.boot") {
				isSpring = true
				break
			}
		}

		if isSpring {
			runtime = "java"
			framework = "Spring Boot (Gradle)"
			healthPath = "/actuator/health"
		} else {
			runtime = "java"
			framework = "Java (Gradle)"
		}
		return
	}

	// 7. Rust Ecosystem
	if _, ok := files["Cargo.toml"]; ok {
		runtime = "rust"
		framework = "Rust"
		pkgManager = "cargo"
		port = 8080
		healthPath = "/"
		healthStrat = models.HealthStrategyHTTP
		buildCmd = "cargo build --release"
		startCmd = "./target/release/server"
		return
	}

	// 8. Ruby Ecosystem
	if gemRaw, ok := files["Gemfile"]; ok {
		runtime = "ruby"
		framework = "Ruby"
		pkgManager = "bundler"
		port = 3000
		healthPath = "/"
		healthStrat = models.HealthStrategyHTTP
		lowerGem := strings.ToLower(string(gemRaw))
		if hasKey(files, "config/routes.rb") || strings.Contains(lowerGem, "rails") {
			framework = "Ruby on Rails"
			startCmd = "bundle exec puma -C config/puma.rb -b tcp://0.0.0.0:3000"
		} else {
			startCmd = "bundle exec rackup -o 0.0.0.0 -p 3000"
		}
		return
	}

	// 9. .NET Ecosystem
	for f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".csproj") {
			runtime = "dotnet"
			framework = ".NET / ASP.NET"
			pkgManager = "dotnet"
			port = 8080
			healthPath = "/health"
			healthStrat = models.HealthStrategyHTTP
			buildCmd = "dotnet publish -c Release -o /app/out"
			startCmd = "dotnet /app/out/app.dll"
			return
		}
	}

	// 10. Static HTML/CSS/JS (Evidence-based check: index.html present, no backend runtime)
	if hasKey(files, "index.html") {
		runtime = "static"
		framework = "Static HTML/CSS/JS"
		port = 80
		healthPath = "/"
		healthStrat = models.HealthStrategyHTTP
		startCmd = "nginx -g 'daemon off;'"
		return
	}

	return
}

// GenerateDockerfile produces an optimized multi-stage Dockerfile based on runtime, port, and commands
func GenerateDockerfile(runtime string, port int, startCmd, pkgManager string) string {
	if port <= 0 {
		port = 8080
	}

	installCmd := "npm install"
	runBuildCmd := "npm run build"
	runStartCmd := "npm start"

	if pkgManager == "pnpm" {
		installCmd = "corepack enable && pnpm install --frozen-lockfile || pnpm install"
		runBuildCmd = "pnpm run build"
		runStartCmd = "pnpm start"
	} else if pkgManager == "yarn" {
		installCmd = "yarn install"
		runBuildCmd = "yarn build"
		runStartCmd = "yarn start"
	} else if pkgManager == "bun" {
		installCmd = "bun install"
		runBuildCmd = "bun run build"
		runStartCmd = "bun run start"
	}

	switch runtime {
	case "static":
		return fmt.Sprintf(`FROM nginx:alpine
WORKDIR /usr/share/nginx/html
COPY . .
EXPOSE %d
CMD ["nginx", "-g", "daemon off;"]
`, port)

	case "nextjs":
		return fmt.Sprintf(`FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json pnpm-lock.yaml* yarn.lock* bun.lock* ./
RUN %s
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN %s

FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=%d
COPY --from=builder /app ./
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, installCmd, runBuildCmd, port, port, runStartCmd)

	case "react-vite", "vue-vite", "svelte-vite":
		return fmt.Sprintf(`FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json pnpm-lock.yaml* yarn.lock* bun.lock* ./
RUN %s
COPY . .
RUN %s

FROM node:20-alpine AS runner
WORKDIR /app
RUN npm install -g serve
COPY --from=builder /app/dist ./dist
ENV PORT=%d
EXPOSE %d
CMD ["serve", "-s", "dist", "-l", "%d"]
`, installCmd, runBuildCmd, port, port, port)

	case "nodejs", "express", "fastify", "nestjs":
		cmd := runStartCmd
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM node:20-alpine
WORKDIR /app
COPY package*.json pnpm-lock.yaml* yarn.lock* bun.lock* ./
RUN %s
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, installCmd, port, port, cmd)

	case "python-fastapi":
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi && \
    pip install --no-cache-dir uvicorn fastapi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "%d"]
`, port, port, port)

	case "python-flask":
		cmd := fmt.Sprintf("gunicorn --bind 0.0.0.0:%d --workers 2 app:app", port)
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi && \
    pip install --no-cache-dir gunicorn
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, cmd)

	case "python-django":
		cmd := fmt.Sprintf("gunicorn --bind 0.0.0.0:%d --workers 2 wsgi:application", port)
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi && \
    pip install --no-cache-dir gunicorn
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, cmd)

	case "python":
		cmd := "python main.py"
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* Pipfile* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, cmd)

	case "go":
		return fmt.Sprintf(`FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/server . || CGO_ENABLED=0 go build -o /app/server ./cmd/... || CGO_ENABLED=0 go build -o /app/server ./...

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server /app/server
ENV PORT=%d
EXPOSE %d
CMD ["/app/server"]
`, port, port)

	case "java":
		if pkgManager == "gradle" {
			return fmt.Sprintf(`FROM gradle:8.5-jdk17-alpine AS builder
WORKDIR /app
COPY build.gradle* settings.gradle* gradlew* ./
COPY gradle ./gradle
COPY src ./src
RUN if [ -f ./gradlew ]; then chmod +x ./gradlew && ./gradlew build -x test; else gradle build -x test; fi && \
    find build/libs -name "*.jar" ! -name "*-plain.jar" -exec cp {} /app/app.jar \;

FROM eclipse-temurin:17-jre-alpine AS runner
WORKDIR /app
COPY --from=builder /app/app.jar /app/app.jar
ENV PORT=%d
EXPOSE %d
CMD ["java", "-jar", "/app/app.jar"]
`, port, port)
		}
		return fmt.Sprintf(`FROM maven:3.9-eclipse-temurin-17-alpine AS builder
WORKDIR /app
COPY pom.xml ./
RUN mvn dependency:go-offline -B || true
COPY src ./src
RUN mvn clean package -DskipTests && \
    find target -maxdepth 1 -name "*.jar" ! -name "*-sources.jar" ! -name "*-javadoc.jar" -exec cp {} /app/app.jar \;

FROM eclipse-temurin:17-jre-alpine AS runner
WORKDIR /app
COPY --from=builder /app/app.jar /app/app.jar
ENV PORT=%d
EXPOSE %d
CMD ["java", "-jar", "/app/app.jar"]
`, port, port)

	case "rust":
		return fmt.Sprintf(`FROM rust:1.75-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml Cargo.lock* ./
COPY src ./src
RUN cargo build --release && \
    find target/release -maxdepth 1 -type f -perm /111 ! -name "*.d" -exec cp {} /app/server \;

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server /app/server
ENV PORT=%d
EXPOSE %d
CMD ["/app/server"]
`, port, port)

	case "php":
		cmd := fmt.Sprintf("php -d variables_order=EGPCS -S 0.0.0.0:%d -t public", port)
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM php:8.2-cli-alpine
WORKDIR /app
COPY composer.json* composer.lock* ./
RUN if [ -f composer.json ]; then curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer && composer install --no-dev --optimize-autoloader; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, cmd)

	case "ruby":
		cmd := fmt.Sprintf("bundle exec puma -C config/puma.rb -b tcp://0.0.0.0:%d", port)
		if startCmd != "" {
			cmd = startCmd
		}
		return fmt.Sprintf(`FROM ruby:3.2-alpine
RUN apk add --no-cache build-base
WORKDIR /app
COPY Gemfile Gemfile.lock* ./
RUN bundle install --without development test
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "%s"]
`, port, port, cmd)

	case "dotnet":
		return fmt.Sprintf(`FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build
WORKDIR /src
COPY *.csproj ./
RUN dotnet restore
COPY . .
RUN dotnet publish -c Release -o /app/publish

FROM mcr.microsoft.com/dotnet/aspnet:8.0 AS final
WORKDIR /app
COPY --from=build /app/publish .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "dotnet $(ls *.dll | head -n 1)"]
`, port, port)

	default:
		return fmt.Sprintf(`FROM alpine:latest
WORKDIR /app
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "echo 'Application started on port %d' && sleep infinity"]
`, port, port, port)
	}
}

func formatRunCommand(pkgManager, script string) string {
	if pkgManager == "pnpm" {
		return "pnpm run " + script
	}
	if pkgManager == "yarn" {
		return "yarn " + script
	}
	if pkgManager == "bun" {
		return "bun run " + script
	}
	if script == "start" {
		return "npm start"
	}
	return "npm run " + script
}

func hasDep(deps map[string]string, name string) bool {
	if deps == nil {
		return false
	}
	_, ok := deps[name]
	return ok
}

func hasKey(m map[string][]byte, key string) bool {
	_, ok := m[key]
	return ok
}

func hasFileWithPrefix(files map[string][]byte, prefix string) bool {
	for f := range files {
		base := filepath.Base(f)
		if strings.HasPrefix(strings.ToLower(base), strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func hasFileWithExt(files map[string][]byte, ext string) bool {
	for f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(strings.ToLower(base), strings.ToLower(ext)) {
			return true
		}
	}
	return false
}

// ProductionRecommendation specifies production-ready commands and build candidates.
type ProductionRecommendation struct {
	ProdBuildCommand string                  `json:"prod_build_command"`
	ProdStartCommand string                  `json:"prod_start_command"`
	DevStartCommand  string                  `json:"dev_start_command"`
	Candidates       []models.BuildCandidate `json:"candidates"`
}

// RecommendProductionExecution cleanly separates technology detection from production execution recommendations.
func RecommendProductionExecution(runtime, framework, pkgManager string, port int, healthPath, healthStrat string) ProductionRecommendation {
	rec := ProductionRecommendation{}
	switch {
	case strings.Contains(runtime, "django") || framework == "Django":
		rec.ProdBuildCommand = "pip install -r requirements.txt gunicorn"
		rec.ProdStartCommand = fmt.Sprintf("gunicorn --bind 0.0.0.0:%d --workers 2 wsgi:application", port)
		rec.DevStartCommand = fmt.Sprintf("python manage.py runserver 0.0.0.0:%d", port)
		rec.Candidates = []models.BuildCandidate{
			{
				ID:              "django-production",
				Strategy:        StrategyAuto,
				Name:            "Django Production (Gunicorn)",
				Description:     "Production WSGI server with Gunicorn and 2 workers",
				Confidence:      0.95,
				BuildCommand:    rec.ProdBuildCommand,
				StartCommand:    rec.ProdStartCommand,
				PackageManager:  pkgManager,
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
			{
				ID:              "django-dev",
				Strategy:        StrategyAuto,
				Name:            "Django Development Server",
				Description:     "Django built-in development server (non-production)",
				Confidence:      0.50,
				BuildCommand:    "",
				StartCommand:    rec.DevStartCommand,
				PackageManager:  pkgManager,
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
		}
	case strings.Contains(runtime, "flask") || framework == "Flask":
		rec.ProdBuildCommand = "pip install -r requirements.txt gunicorn"
		rec.ProdStartCommand = fmt.Sprintf("gunicorn --bind 0.0.0.0:%d --workers 2 app:app", port)
		rec.DevStartCommand = "python app.py"
		rec.Candidates = []models.BuildCandidate{
			{
				ID:              "flask-production",
				Strategy:        StrategyAuto,
				Name:            "Flask Production (Gunicorn)",
				Description:     "Production WSGI server with Gunicorn and 2 workers",
				Confidence:      0.95,
				BuildCommand:    rec.ProdBuildCommand,
				StartCommand:    rec.ProdStartCommand,
				PackageManager:  pkgManager,
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
			{
				ID:              "flask-dev",
				Strategy:        StrategyAuto,
				Name:            "Flask Development Server",
				Description:     "Flask built-in development server (non-production)",
				Confidence:      0.50,
				BuildCommand:    "",
				StartCommand:    rec.DevStartCommand,
				PackageManager:  pkgManager,
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
		}
	case framework == "Laravel":
		rec.ProdBuildCommand = "composer install --no-dev --optimize-autoloader"
		rec.ProdStartCommand = fmt.Sprintf("php -d variables_order=EGPCS -S 0.0.0.0:%d -t public", port)
		rec.DevStartCommand = fmt.Sprintf("php artisan serve --host=0.0.0.0 --port=%d", port)
		rec.Candidates = []models.BuildCandidate{
			{
				ID:              "laravel-production",
				Strategy:        StrategyAuto,
				Name:            "Laravel Production (Optimized)",
				Description:     "Production standalone public server with optimized autoloader",
				Confidence:      0.95,
				BuildCommand:    rec.ProdBuildCommand,
				StartCommand:    rec.ProdStartCommand,
				PackageManager:  "composer",
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
			{
				ID:              "laravel-dev",
				Strategy:        StrategyAuto,
				Name:            "Laravel Artisan Server (Development)",
				Description:     "Artisan development serve command (non-production)",
				Confidence:      0.50,
				BuildCommand:    "",
				StartCommand:    rec.DevStartCommand,
				PackageManager:  "composer",
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
		}
	case framework == "Ruby on Rails":
		rec.ProdBuildCommand = "bundle install --without development test"
		rec.ProdStartCommand = fmt.Sprintf("bundle exec puma -C config/puma.rb -b tcp://0.0.0.0:%d", port)
		rec.DevStartCommand = fmt.Sprintf("bundle exec rails server -b 0.0.0.0 -p %d", port)
		rec.Candidates = []models.BuildCandidate{
			{
				ID:              "rails-production",
				Strategy:        StrategyAuto,
				Name:            "Ruby on Rails Production (Puma)",
				Description:     "Production Puma web server",
				Confidence:      0.95,
				BuildCommand:    rec.ProdBuildCommand,
				StartCommand:    rec.ProdStartCommand,
				PackageManager:  "bundler",
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
			{
				ID:              "rails-dev",
				Strategy:        StrategyAuto,
				Name:            "Rails Server (Development)",
				Description:     "Rails built-in development server (non-production)",
				Confidence:      0.50,
				BuildCommand:    "",
				StartCommand:    rec.DevStartCommand,
				PackageManager:  "bundler",
				SuggestedPort:   port,
				HealthCheckPath: healthPath,
				HealthStrategy:  healthStrat,
			},
		}
	}
	return rec
}
