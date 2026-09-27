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

func TestGenerateDockerfile(t *testing.T) {
	dfNext := GenerateDockerfile("nextjs", 3000, "npm start")
	if !strings.Contains(dfNext, "EXPOSE 3000") || !strings.Contains(dfNext, "npm run build") {
		t.Fatalf("unexpected Dockerfile for Next.js: %s", dfNext)
	}

	dfGo := GenerateDockerfile("go", 8080, "/app/server")
	if !strings.Contains(dfGo, "go build") || !strings.Contains(dfGo, "EXPOSE 8080") {
		t.Fatalf("unexpected Dockerfile for Go: %s", dfGo)
	}

	dfPy := GenerateDockerfile("python-fastapi", 8000, "")
	if !strings.Contains(dfPy, "uvicorn") || !strings.Contains(dfPy, "EXPOSE 8000") {
		t.Fatalf("unexpected Dockerfile for FastAPI: %s", dfPy)
	}
}
