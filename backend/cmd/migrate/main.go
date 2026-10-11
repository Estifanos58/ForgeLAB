package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file
	if err := godotenv.Load("../.env"); err != nil {
		godotenv.Load(".env")
	}

	direction := flag.String("direction", "up", "Migration direction: up or down")
	flag.Parse()

	// Also support positional argument
	if flag.NArg() > 0 {
		*direction = flag.Arg(0)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" || databaseURL == "[REDACTED]" || !strings.Contains(databaseURL, "://") {
		databaseURL = os.Getenv("POSTGRES_URL")
	}
	if databaseURL == "" || databaseURL == "[REDACTED]" || !strings.Contains(databaseURL, "://") {
		host := os.Getenv("POSTGRES_HOST")
		if host == "" {
			host = os.Getenv("DB_HOST")
		}
		if host == "" {
			if _, err := net.LookupHost("postgres"); err == nil {
				host = "postgres"
			} else {
				host = "localhost"
			}
		}
		port := os.Getenv("POSTGRES_PORT")
		if port == "" {
			port = os.Getenv("DB_PORT")
		}
		if port == "" {
			port = "5432"
		}
		user := os.Getenv("POSTGRES_USER")
		if user == "" || user == "[REDACTED]" {
			user = os.Getenv("DB_USER")
		}
		if user == "" || user == "[REDACTED]" {
			user = "forgelab"
		}
		password := os.Getenv("POSTGRES_PASSWORD")
		if password == "" || password == "[REDACTED]" {
			password = os.Getenv("DB_PASSWORD")
		}
		if password == "" || password == "[REDACTED]" {
			password = "forgelab_dev_password"
		}
		dbName := os.Getenv("POSTGRES_DB")
		if dbName == "" || dbName == "[REDACTED]" {
			dbName = os.Getenv("DB_NAME")
		}
		if dbName == "" || dbName == "[REDACTED]" {
			dbName = "forgelab"
		}
		databaseURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, dbName)
	}

	migrationsPath := "file://migrations"

	var m *migrate.Migrate
	var err error
	for attempts := 1; attempts <= 15; attempts++ {
		m, err = migrate.New(migrationsPath, databaseURL)
		if err == nil {
			break
		}
		log.Printf("Waiting for database to accept connections (attempt %d/15): %v", attempts, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("Failed to create migrate instance after retries: %v", err)
	}
	defer m.Close()

	switch *direction {
	case "up":
		fmt.Println("Running migrations UP...")
		if err := m.Up(); err != nil {
			if err == migrate.ErrNoChange {
				fmt.Println("No new migrations to apply.")
				return
			}
			log.Fatalf("Migration UP failed: %v", err)
		}
		fmt.Println("Migrations applied successfully.")

	case "down":
		fmt.Println("Running migrations DOWN...")
		if err := m.Down(); err != nil {
			if err == migrate.ErrNoChange {
				fmt.Println("No migrations to roll back.")
				return
			}
			log.Fatalf("Migration DOWN failed: %v", err)
		}
		fmt.Println("Migrations rolled back successfully.")

	default:
		log.Fatalf("Unknown direction: %s (use 'up' or 'down')", *direction)
	}

	version, dirty, err := m.Version()
	if err != nil && err != migrate.ErrNilVersion {
		log.Printf("Warning: Could not get version: %v", err)
	} else if err == nil {
		fmt.Printf("Current version: %d (dirty: %v)\n", version, dirty)
	}
}
