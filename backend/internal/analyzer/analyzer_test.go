package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeRepository_FrontendOnly(t *testing.T) {
	tempDir := t.TempDir()

	// Create Next.js project with pnpm
	pkgJSON := `{
		"name": "my-frontend",
		"dependencies": {
			"next": "14.0.0",
			"react": "18.2.0"
		},
		"scripts": {
			"build": "next build",
			"start": "next start"
		}
	}`
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(pkgJSON), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "pnpm-lock.yaml"), []byte("lockfileVersion: 5.4"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "next.config.js"), []byte("module.exports = {}"), 0644)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(res.Services))
	}

	svc := res.Services[0]
	if svc.Role != "frontend" {
		t.Errorf("expected role 'frontend', got %q", svc.Role)
	}
	if svc.RuntimeType != "nextjs" {
		t.Errorf("expected runtime 'nextjs', got %q", svc.RuntimeType)
	}
	if svc.PackageManager != "pnpm" {
		t.Errorf("expected package manager 'pnpm', got %q", svc.PackageManager)
	}
	if svc.InternalPort != 3000 {
		t.Errorf("expected port 3000, got %d", svc.InternalPort)
	}
	if len(svc.BuildCandidates) == 0 {
		t.Errorf("expected build candidates, got none")
	}
}

func TestAnalyzeRepository_BackendOnly_Maven(t *testing.T) {
	tempDir := t.TempDir()

	pomXML := `<project>
		<modelVersion>4.0.0</modelVersion>
		<groupId>com.example</groupId>
		<artifactId>my-backend</artifactId>
		<dependencies>
			<dependency>
				<groupId>org.springframework.boot</groupId>
				<artifactId>spring-boot-starter-web</artifactId>
			</dependency>
		</dependencies>
	</project>`
	_ = os.WriteFile(filepath.Join(tempDir, "pom.xml"), []byte(pomXML), 0644)
	_ = os.MkdirAll(filepath.Join(tempDir, "src", "main", "java"), 0755)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(res.Services))
	}

	svc := res.Services[0]
	if svc.Role != "backend" {
		t.Errorf("expected role 'backend', got %q", svc.Role)
	}
	if svc.Framework != "Spring Boot (Maven)" {
		t.Errorf("expected framework 'Spring Boot (Maven)', got %q", svc.Framework)
	}
	if svc.PackageManager != "maven" {
		t.Errorf("expected package manager 'maven', got %q", svc.PackageManager)
	}
	if svc.InternalPort != 8080 {
		t.Errorf("expected port 8080, got %d", svc.InternalPort)
	}
}

func TestAnalyzeRepository_BackendOnly_Gradle(t *testing.T) {
	tempDir := t.TempDir()

	buildGradle := `plugins {
		id 'org.springframework.boot' version '3.2.0'
		id 'java'
	}`
	_ = os.WriteFile(filepath.Join(tempDir, "build.gradle"), []byte(buildGradle), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "gradlew"), []byte("#!/bin/sh"), 0755)
	_ = os.MkdirAll(filepath.Join(tempDir, "src"), 0755)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(res.Services))
	}

	svc := res.Services[0]
	if svc.Role != "backend" {
		t.Errorf("expected role 'backend', got %q", svc.Role)
	}
	if svc.PackageManager != "gradle" {
		t.Errorf("expected package manager 'gradle', got %q", svc.PackageManager)
	}
	if svc.Framework != "Spring Boot (Gradle)" {
		t.Errorf("expected framework 'Spring Boot (Gradle)', got %q", svc.Framework)
	}
}

func TestAnalyzeRepository_FrontendAndBackend(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Frontend directory: Vite / React
	frontendDir := filepath.Join(tempDir, "frontend")
	_ = os.MkdirAll(frontendDir, 0755)
	vitePkg := `{
		"name": "web-ui",
		"devDependencies": {
			"vite": "5.0.0"
		},
		"dependencies": {
			"react": "18.2.0"
		}
	}`
	_ = os.WriteFile(filepath.Join(frontendDir, "package.json"), []byte(vitePkg), 0644)
	_ = os.WriteFile(filepath.Join(frontendDir, "vite.config.ts"), []byte("export default {}"), 0644)
	_ = os.WriteFile(filepath.Join(frontendDir, "yarn.lock"), []byte(""), 0644)

	// 2. Backend directory: Go Chi
	backendDir := filepath.Join(tempDir, "backend")
	_ = os.MkdirAll(backendDir, 0755)
	goMod := `module github.com/example/api

go 1.22

require github.com/go-chi/chi/v5 v5.0.0
`
	_ = os.WriteFile(filepath.Join(backendDir, "go.mod"), []byte(goMod), 0644)
	_ = os.WriteFile(filepath.Join(backendDir, "main.go"), []byte("package main\nfunc main() {}"), 0644)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(res.Services))
	}

	roles := make(map[string]ServiceDefinition)
	for _, svc := range res.Services {
		roles[svc.Role] = svc
	}

	fe, ok := roles["frontend"]
	if !ok {
		t.Fatalf("missing frontend service")
	}
	if fe.RuntimeType != "react-vite" {
		t.Errorf("expected frontend runtime 'react-vite', got %q", fe.RuntimeType)
	}
	if fe.PackageManager != "yarn" {
		t.Errorf("expected package manager 'yarn', got %q", fe.PackageManager)
	}

	be, ok := roles["backend"]
	if !ok {
		t.Fatalf("missing backend service")
	}
	if be.RuntimeType != "go" {
		t.Errorf("expected backend runtime 'go', got %q", be.RuntimeType)
	}
	if be.Framework != "Go (Chi)" {
		t.Errorf("expected framework 'Go (Chi)', got %q", be.Framework)
	}
}

func TestAnalyzeRepository_NestedApps(t *testing.T) {
	tempDir := t.TempDir()

	// Nested monorepo structure: apps/web and apps/api
	webDir := filepath.Join(tempDir, "apps", "web")
	_ = os.MkdirAll(webDir, 0755)
	_ = os.WriteFile(filepath.Join(webDir, "package.json"), []byte(`{"name":"web","dependencies":{"next":"14.0.0"}}`), 0644)

	apiDir := filepath.Join(tempDir, "apps", "api")
	_ = os.MkdirAll(apiDir, 0755)
	_ = os.WriteFile(filepath.Join(apiDir, "requirements.txt"), []byte("fastapi==0.100.0\nuvicorn==0.22.0"), 0644)
	_ = os.WriteFile(filepath.Join(apiDir, "main.py"), []byte("from fastapi import FastAPI\napp = FastAPI()"), 0644)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 2 {
		t.Fatalf("expected 2 services in nested apps, got %d", len(res.Services))
	}

	names := make(map[string]ServiceDefinition)
	for _, svc := range res.Services {
		names[svc.Name] = svc
	}

	if web, ok := names["web"]; !ok || web.RuntimeType != "nextjs" {
		t.Errorf("expected web service with nextjs, got %+v", web)
	}
	if api, ok := names["api"]; !ok || api.RuntimeType != "python-fastapi" {
		t.Errorf("expected api service with python-fastapi, got %+v", api)
	}
}

func TestAnalyzeRepository_DockerfilePresentVsAbsent(t *testing.T) {
	tempDir := t.TempDir()

	// Node.js project with an existing Dockerfile
	_ = os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(`{"name":"app","scripts":{"start":"node index.js"}}`), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "Dockerfile"), []byte("FROM alpine\nEXPOSE 9090\nCMD [\"sh\"]"), 0644)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(res.Services))
	}

	svc := res.Services[0]
	if svc.InternalPort != 9090 {
		t.Errorf("expected port 9090 parsed from EXPOSE in Dockerfile, got %d", svc.InternalPort)
	}

	// Candidates must contain both Dockerfile and Auto
	var hasDockerfileCandidate, hasAutoCandidate bool
	for _, c := range svc.BuildCandidates {
		if c.Strategy == "dockerfile" {
			hasDockerfileCandidate = true
		}
		if c.Strategy == "auto" {
			hasAutoCandidate = true
		}
	}

	if !hasDockerfileCandidate {
		t.Errorf("expected existing Dockerfile candidate")
	}
	if !hasAutoCandidate {
		t.Errorf("expected auto generated candidate alongside existing Dockerfile")
	}
}

func TestAnalyzeRepository_PrunedDirectoriesIgnored(t *testing.T) {
	tempDir := t.TempDir()

	// Main Go project
	_ = os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module myapp\ngo 1.22"), 0644)

	// Subfolder inside node_modules that looks like a project
	nmDir := filepath.Join(tempDir, "node_modules", "some-dep")
	_ = os.MkdirAll(nmDir, 0755)
	_ = os.WriteFile(filepath.Join(nmDir, "package.json"), []byte(`{"name":"some-dep"}`), 0644)

	// Subfolder in .git
	gitDir := filepath.Join(tempDir, ".git", "hooks")
	_ = os.MkdirAll(gitDir, 0755)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must NOT discover some-dep inside node_modules!
	if len(res.Services) != 1 {
		t.Fatalf("expected exactly 1 service (ignored node_modules), got %d", len(res.Services))
	}
	if res.Services[0].RuntimeType != "go" {
		t.Errorf("expected Go service, got %q", res.Services[0].RuntimeType)
	}
}

func TestAnalyzeRepository_UnknownFramework(t *testing.T) {
	tempDir := t.TempDir()

	// Just a script file
	_ = os.WriteFile(filepath.Join(tempDir, "run.sh"), []byte("#!/bin/sh\necho hello"), 0755)

	res, err := AnalyzeRepository(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Services) != 1 {
		t.Fatalf("expected 1 fallback service, got %d", len(res.Services))
	}
	svc := res.Services[0]
	if svc.RuntimeType != "generic" {
		t.Errorf("expected generic runtime, got %q", svc.RuntimeType)
	}
}
