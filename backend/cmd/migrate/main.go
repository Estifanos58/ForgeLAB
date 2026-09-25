package main

import (
	"flag"
	"fmt"
	"log"
	"os"

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
	if databaseURL == "" {
		databaseURL = "postgres://forgelab:forgelab_dev_password@localhost:5432/forgelab?sslmode=disable"
	}

	migrationsPath := "file://migrations"

	m, err := migrate.New(migrationsPath, databaseURL)
	if err != nil {
		log.Fatalf("Failed to create migrate instance: %v", err)
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
