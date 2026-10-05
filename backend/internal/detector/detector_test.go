package detector

import (
	"strings"
	"testing"
)

func TestDetectFromFiles_Dockerfile(t *testing.T) {
	files := map[string][]byte{
		"Dockerfile": []byte("FROM alpine\nEXPOSE 9000\nCMD [\"sh\"]"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "dockerfile" {
		t.Fatalf("expected dockerfile, got %s", res.Runtime)
	}
	if res.SuggestedPort != 9000 {
		t.Fatalf("expected port 9000, got %d", res.SuggestedPort)
	}
	if res.BuildStrategy != "dockerfile" {
		t.Fatalf("expected build strategy dockerfile, got %s", res.BuildStrategy)
	}
}

func TestDetectFromFiles_NextJS(t *testing.T) {
	files := map[string][]byte{
		"package.json":   []byte(`{"dependencies": {"next": "14.2.0", "react": "18.2.0"}}`),
		"next.config.ts": []byte("export default {};"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "nextjs" {
		t.Fatalf("expected nextjs, got %s", res.Runtime)
	}
	if res.SuggestedPort != 3000 {
		t.Fatalf("expected port 3000, got %d", res.SuggestedPort)
	}
	if res.BuildStrategy != "auto" {
		t.Fatalf("expected auto strategy, got %s", res.BuildStrategy)
	}
	if res.StartCommand != "npm start" {
		t.Fatalf("expected npm start, got %s", res.StartCommand)
	}
}

func TestDetectFromFiles_Vite(t *testing.T) {
	files := map[string][]byte{
		"package.json":   []byte(`{"dependencies": {"react": "^18.2.0"}, "devDependencies": {"vite": "^5.0.0"}}`),
		"vite.config.ts": []byte("export default {};"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "react-vite" {
		t.Fatalf("expected react-vite, got %s", res.Runtime)
	}
	if res.SuggestedPort != 3000 {
		t.Fatalf("expected port 3000, got %d", res.SuggestedPort)
	}
}

func TestDetectFromFiles_PythonFastAPI(t *testing.T) {
	files := map[string][]byte{
		"requirements.txt": []byte("fastapi==0.110.0\nuvicorn==0.28.0"),
		"main.py":          []byte("from fastapi import FastAPI\napp = FastAPI()"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "python-fastapi" {
		t.Fatalf("expected python-fastapi, got %s", res.Runtime)
	}
	if res.SuggestedPort != 8000 {
		t.Fatalf("expected port 8000, got %d", res.SuggestedPort)
	}
	if !strings.Contains(res.StartCommand, "uvicorn") {
		t.Fatalf("expected uvicorn in start command, got %s", res.StartCommand)
	}
}

func TestDetectFromFiles_PythonFlask(t *testing.T) {
	files := map[string][]byte{
		"requirements.txt": []byte("flask==3.0.0\ngunicorn==21.2.0"),
		"app.py":           []byte("from flask import Flask\napp = Flask(__name__)"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "python-flask" {
		t.Fatalf("expected python-flask, got %s", res.Runtime)
	}
	if res.SuggestedPort != 5000 {
		t.Fatalf("expected port 5000, got %d", res.SuggestedPort)
	}
}

func TestDetectFromFiles_PythonDjango(t *testing.T) {
	files := map[string][]byte{
		"requirements.txt": []byte("django==5.0.2"),
		"manage.py":        []byte("#!/usr/bin/env python"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "python-django" {
		t.Fatalf("expected python-django, got %s", res.Runtime)
	}
	if res.SuggestedPort != 8000 {
		t.Fatalf("expected port 8000, got %d", res.SuggestedPort)
	}
	if !strings.Contains(res.StartCommand, "manage.py") && !strings.Contains(res.StartCommand, "gunicorn") {
		t.Fatalf("expected manage.py or gunicorn in start command, got %s", res.StartCommand)
	}
}

func TestDetectFromFiles_Go(t *testing.T) {
	files := map[string][]byte{
		"go.mod":  []byte("module example.com/app\n\ngo 1.22"),
		"main.go": []byte("package main\nfunc main() {}"),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "go" {
		t.Fatalf("expected go, got %s", res.Runtime)
	}
	if res.SuggestedPort != 8080 {
		t.Fatalf("expected port 8080, got %d", res.SuggestedPort)
	}
	if res.BuildStrategy != "auto" {
		t.Fatalf("expected auto strategy, got %s", res.BuildStrategy)
	}
}

func TestDetectFromFiles_Java_Maven(t *testing.T) {
	files := map[string][]byte{
		"pom.xml": []byte(`<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>demo</artifactId></project>`),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "java" {
		t.Fatalf("expected java, got %s", res.Runtime)
	}
	// Verify no shell glob in StartCommand
	if strings.Contains(res.StartCommand, "*") {
		t.Fatalf("StartCommand must not contain wildcard globs: %s", res.StartCommand)
	}
	if res.StartCommand != "java -jar /app/app.jar" {
		t.Fatalf("expected java -jar /app/app.jar, got %s", res.StartCommand)
	}
}

func TestDetectFromFiles_Java_Gradle(t *testing.T) {
	files := map[string][]byte{
		"build.gradle": []byte(`plugins { id 'org.springframework.boot' version '3.2.0' }`),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "java" {
		t.Fatalf("expected java, got %s", res.Runtime)
	}
	// Verify no shell glob in StartCommand
	if strings.Contains(res.StartCommand, "*") {
		t.Fatalf("StartCommand must not contain wildcard globs: %s", res.StartCommand)
	}
	if res.StartCommand != "java -jar /app/app.jar" {
		t.Fatalf("expected java -jar /app/app.jar, got %s", res.StartCommand)
	}
}

func TestDetectFromFiles_Rust(t *testing.T) {
	files := map[string][]byte{
		"Cargo.toml": []byte(`[package]
name = "my-service"
version = "0.1.0"
edition = "2021"
`),
	}
	res := DetectFromFiles(files)
	if res.Runtime != "rust" {
		t.Fatalf("expected rust, got %s", res.Runtime)
	}
}

func TestGenerateDockerfile(t *testing.T) {
	runtimes := []string{
		"nextjs",
		"react-vite",
		"nodejs",
		"python-fastapi",
		"python-flask",
		"python-django",
		"python",
		"go",
		"java",
		"java-maven",
		"java-gradle",
		"rust",
	}

	for _, rt := range runtimes {
		df := GenerateDockerfile(rt, 8080, "")
		if df == "" {
			t.Fatalf("GenerateDockerfile returned empty for %s", rt)
		}
		if !strings.Contains(df, "EXPOSE 8080") {
			t.Fatalf("GenerateDockerfile(%s) missing EXPOSE 8080", rt)
		}

		// Security & correctness check: No exec-form CMD should contain a raw shell glob '*'
		for _, line := range strings.Split(df, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "CMD [") {
				if strings.Contains(trimmed, "*") {
					t.Fatalf("Runtime %s has shell glob in exec-form CMD: %s", rt, trimmed)
				}
			}
		}
	}
}

func TestGenerateDockerfile_JavaMaven_Valid(t *testing.T) {
	df := GenerateDockerfile("java", 8080, "")
	if !strings.Contains(df, "maven") || !strings.Contains(df, "app.jar") {
		t.Fatalf("expected maven and app.jar in Dockerfile, got: %s", df)
	}
	if strings.Contains(df, "target/*.jar") && strings.Contains(df, "CMD [") {
		t.Fatalf("found unexpanded glob in exec CMD: %s", df)
	}
}

func TestGenerateDockerfile_Rust_Valid(t *testing.T) {
	df := GenerateDockerfile("rust", 8080, "")
	if !strings.Contains(df, "cargo build --release") {
		t.Fatalf("expected cargo build in Rust Dockerfile: %s", df)
	}
	// Must not have COPY --from=builder /app/target/release/* /app/server
	if strings.Contains(df, "COPY --from=builder /app/target/release/* /app/server") {
		t.Fatalf("Rust Dockerfile has unsafe wildcard directory copy: %s", df)
	}
	if !strings.Contains(df, `CMD ["/app/server"]`) {
		t.Fatalf("expected CMD [/app/server] in Rust Dockerfile: %s", df)
	}
}
