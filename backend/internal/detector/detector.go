package detector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DetectionResult represents the detected runtime, build system, and recommendations.
type DetectionResult struct {
	Runtime         string   `json:"runtime"`
	Framework       string   `json:"framework"`
	BuildStrategy   string   `json:"build_strategy"`
	SuggestedPort   int      `json:"suggested_port"`
	BuildCommand    string   `json:"build_command"`
	StartCommand    string   `json:"start_command"`
	HealthCheckPath string   `json:"health_check_path"`
	HealthStrategy  string   `json:"health_strategy"`
	DetectedFiles   []string `json:"detected_files"`
}

var exposeRegex = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(\d+)`)

// Detect inspects a local source directory and determines the application characteristics.
func Detect(sourceDir string) (*DetectionResult, error) {
	files := make(map[string][]byte)

	// Scan top-level and first-level files
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read source directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Read immediate subfiles for things like src/main.go or app/main.py
			subEntries, _ := os.ReadDir(filepath.Join(sourceDir, entry.Name()))
			for _, sub := range subEntries {
				if !sub.IsDir() {
					relPath := filepath.Join(entry.Name(), sub.Name())
					// Only read small config/indicator files
					if isIndicatorFile(sub.Name()) {
						content, _ := os.ReadFile(filepath.Join(sourceDir, relPath))
						files[relPath] = content
					}
				}
			}
			continue
		}

		name := entry.Name()
		if isIndicatorFile(name) {
			content, _ := os.ReadFile(filepath.Join(sourceDir, name))
			files[name] = content
		}
	}

	res := DetectFromFiles(files)
	return res, nil
}

func isIndicatorFile(name string) bool {
	lower := strings.ToLower(name)
	indicators := []string{
		"dockerfile", "package.json", "next.config.js", "next.config.mjs", "next.config.ts",
		"vite.config.js", "vite.config.ts", "vite.config.mjs", "requirements.txt",
		"pyproject.toml", "pipfile", "go.mod", "pom.xml", "build.gradle",
		"build.gradle.kts", "cargo.toml", "gemfile", "composer.json", "main.go",
		"main.py", "app.py", "manage.py",
	}
	for _, ind := range indicators {
		if lower == ind {
			return true
		}
	}
	return false
}

// DetectFromFiles runs detection from an in-memory map of filename -> content.
func DetectFromFiles(files map[string][]byte) *DetectionResult {
	var detectedFiles []string
	for f := range files {
		detectedFiles = append(detectedFiles, f)
	}

	// 1. Dockerfile check
	for f, content := range files {
		if strings.EqualFold(filepath.Base(f), "dockerfile") {
			port := 8080
			for _, line := range strings.Split(string(content), "\n") {
				if match := exposeRegex.FindStringSubmatch(strings.TrimSpace(line)); len(match) > 1 {
					if p, err := strconv.Atoi(match[1]); err == nil && p > 0 && p <= 65535 {
						port = p
						break
					}
				}
			}
			return &DetectionResult{
				Runtime:         "dockerfile",
				Framework:       "Dockerfile",
				BuildStrategy:   "dockerfile",
				SuggestedPort:   port,
				BuildCommand:    "",
				StartCommand:    "",
				HealthCheckPath: "/health",
				HealthStrategy:  "auto",
				DetectedFiles:   detectedFiles,
			}
		}
	}

	// 2. Next.js Check
	isNext := false
	if _, ok := files["next.config.js"]; ok {
		isNext = true
	} else if _, ok := files["next.config.mjs"]; ok {
		isNext = true
	} else if _, ok := files["next.config.ts"]; ok {
		isNext = true
	} else if pkgContent, ok := files["package.json"]; ok {
		if strings.Contains(string(pkgContent), `"next"`) {
			isNext = true
		}
	}

	if isNext {
		return &DetectionResult{
			Runtime:         "nextjs",
			Framework:       "Next.js",
			BuildStrategy:   "auto",
			SuggestedPort:   3000,
			BuildCommand:    "npm run build",
			StartCommand:    "npm start",
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// 3. Vite / React Frontend
	isVite := false
	if _, ok := files["vite.config.js"]; ok {
		isVite = true
	} else if _, ok := files["vite.config.ts"]; ok {
		isVite = true
	} else if _, ok := files["vite.config.mjs"]; ok {
		isVite = true
	} else if pkgContent, ok := files["package.json"]; ok {
		if strings.Contains(string(pkgContent), `"vite"`) {
			isVite = true
		}
	}

	if isVite {
		return &DetectionResult{
			Runtime:         "react-vite",
			Framework:       "Vite / React",
			BuildStrategy:   "auto",
			SuggestedPort:   3000,
			BuildCommand:    "npm run build",
			StartCommand:    "npx serve -s dist -l 3000",
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// 4. Generic Node.js
	if pkgContent, ok := files["package.json"]; ok {
		buildCommand := ""
		startCommand := "node index.js"

		var pkg struct {
			Scripts map[string]string `json:"scripts"`
			Main    string            `json:"main"`
		}
		if err := json.Unmarshal(pkgContent, &pkg); err == nil {
			if _, ok := pkg.Scripts["build"]; ok {
				buildCommand = "npm run build"
			}
			if _, ok := pkg.Scripts["start"]; ok {
				startCommand = "npm start"
			} else if pkg.Main != "" {
				startCommand = "node " + pkg.Main
			}
		}

		return &DetectionResult{
			Runtime:         "nodejs",
			Framework:       "Node.js",
			BuildStrategy:   "auto",
			SuggestedPort:   3000,
			BuildCommand:    buildCommand,
			StartCommand:    startCommand,
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// 5. Python (FastAPI, Flask, Django, Generic)
	var pyReqs string
	if c, ok := files["requirements.txt"]; ok {
		pyReqs = string(c)
	} else if c, ok := files["pyproject.toml"]; ok {
		pyReqs = string(c)
	} else if c, ok := files["Pipfile"]; ok {
		pyReqs = string(c)
	}

	lowerPyReqs := strings.ToLower(pyReqs)
	if strings.Contains(lowerPyReqs, "fastapi") || strings.Contains(lowerPyReqs, "uvicorn") {
		return &DetectionResult{
			Runtime:         "python-fastapi",
			Framework:       "FastAPI",
			BuildStrategy:   "auto",
			SuggestedPort:   8000,
			BuildCommand:    "",
			StartCommand:    "uvicorn main:app --host 0.0.0.0 --port 8000",
			HealthCheckPath: "/docs",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	if strings.Contains(lowerPyReqs, "flask") {
		return &DetectionResult{
			Runtime:         "python-flask",
			Framework:       "Flask",
			BuildStrategy:   "auto",
			SuggestedPort:   5000,
			BuildCommand:    "",
			StartCommand:    "python app.py",
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	if strings.Contains(lowerPyReqs, "django") || hasFile(files, "manage.py") {
		return &DetectionResult{
			Runtime:         "python-django",
			Framework:       "Django",
			BuildStrategy:   "auto",
			SuggestedPort:   8000,
			BuildCommand:    "",
			StartCommand:    "python manage.py runserver 0.0.0.0:8000",
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	if pyReqs != "" || hasFile(files, "main.py") || hasFile(files, "app.py") {
		start := "python main.py"
		if hasFile(files, "app.py") {
			start = "python app.py"
		}
		return &DetectionResult{
			Runtime:         "python",
			Framework:       "Python",
			BuildStrategy:   "auto",
			SuggestedPort:   8000,
			BuildCommand:    "",
			StartCommand:    start,
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// 6. Go
	if _, ok := files["go.mod"]; ok {
		return &DetectionResult{
			Runtime:         "go",
			Framework:       "Go",
			BuildStrategy:   "auto",
			SuggestedPort:   8080,
			BuildCommand:    "go build -o /app/server .",
			StartCommand:    "/app/server",
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// 7. Java (Maven or Gradle)
	if _, ok := files["pom.xml"]; ok {
		return &DetectionResult{
			Runtime:         "java",
			Framework:       "Java (Maven)",
			BuildStrategy:   "auto",
			SuggestedPort:   8080,
			BuildCommand:    "mvn clean package -DskipTests",
			StartCommand:    "java -jar target/*.jar",
			HealthCheckPath: "/health",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}
	if hasFile(files, "build.gradle") || hasFile(files, "build.gradle.kts") {
		return &DetectionResult{
			Runtime:         "java",
			Framework:       "Java (Gradle)",
			BuildStrategy:   "auto",
			SuggestedPort:   8080,
			BuildCommand:    "./gradlew build -x test",
			StartCommand:    "java -jar build/libs/*.jar",
			HealthCheckPath: "/health",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// 8. Rust
	if _, ok := files["Cargo.toml"]; ok {
		return &DetectionResult{
			Runtime:         "rust",
			Framework:       "Rust",
			BuildStrategy:   "auto",
			SuggestedPort:   8080,
			BuildCommand:    "cargo build --release",
			StartCommand:    "./target/release/server",
			HealthCheckPath: "/",
			HealthStrategy:  "http",
			DetectedFiles:   detectedFiles,
		}
	}

	// Default Fallback
	return &DetectionResult{
		Runtime:         "generic",
		Framework:       "Generic Application",
		BuildStrategy:   "auto",
		SuggestedPort:   8080,
		BuildCommand:    "",
		StartCommand:    "",
		HealthCheckPath: "/health",
		HealthStrategy:  "auto",
		DetectedFiles:   detectedFiles,
	}
}

func hasFile(files map[string][]byte, target string) bool {
	for f := range files {
		if strings.EqualFold(filepath.Base(f), target) {
			return true
		}
	}
	return false
}

// GenerateDockerfile produces an optimized multi-stage Dockerfile string based on the detected runtime.
func GenerateDockerfile(runtime string, port int, startCmd string) string {
	if port <= 0 {
		port = 8080
	}

	switch runtime {
	case "nextjs":
		return fmt.Sprintf(`FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=%d
COPY --from=builder /app ./
EXPOSE %d
CMD ["npm", "start"]
`, port, port)

	case "react-vite":
		return fmt.Sprintf(`FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
RUN npm run build

FROM node:20-alpine AS runner
WORKDIR /app
RUN npm install -g serve
COPY --from=builder /app/dist ./dist
ENV PORT=%d
EXPOSE %d
CMD ["serve", "-s", "dist", "-l", "%d"]
`, port, port, port)

	case "nodejs":
		cmd := `CMD ["npm", "start"]`
		if startCmd != "" {
			cmd = fmt.Sprintf(`CMD %s`, formatCommand(startCmd))
		}
		return fmt.Sprintf(`FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
ENV PORT=%d
EXPOSE %d
%s
`, port, port, cmd)

	case "python-fastapi":
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt pyproject.toml* ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; elif [ -f pyproject.toml ]; then pip install --no-cache-dir .; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "%d"]
`, port, port, port)

	case "python-flask":
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["python", "app.py"]
`, port, port)

	case "python", "python-django":
		cmd := `CMD ["python", "main.py"]`
		if startCmd != "" {
			cmd = fmt.Sprintf(`CMD %s`, formatCommand(startCmd))
		}
		return fmt.Sprintf(`FROM python:3.11-slim
WORKDIR /app
COPY requirements*.txt ./
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi
COPY . .
ENV PORT=%d
EXPOSE %d
%s
`, port, port, cmd)

	case "go":
		return fmt.Sprintf(`FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/server .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server /app/server
ENV PORT=%d
EXPOSE %d
CMD ["/app/server"]
`, port, port)

	case "rust":
		return fmt.Sprintf(`FROM rust:1.75-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml Cargo.lock* ./
COPY src ./src
RUN cargo build --release

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/target/release/* /app/server
ENV PORT=%d
EXPOSE %d
CMD ["/app/server"]
`, port, port)

	default:
		// Generic Node/Static fallback
		return fmt.Sprintf(`FROM alpine:latest
WORKDIR /app
COPY . .
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-c", "echo 'Application started on port %d' && sleep infinity"]
`, port, port, port)
	}
}

func formatCommand(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return `["sh"]`
	}
	b, _ := json.Marshal(parts)
	return string(b)
}
