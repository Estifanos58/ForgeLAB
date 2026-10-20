package docker

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/forgelab/backend/internal/logging"
	"github.com/forgelab/backend/internal/models"
)

// resolveInterServiceConnectionEnv computes the standard connection environment variables
// (e.g. DB_URL, REDIS_URL, BACKEND_INTERNAL_URL, NEXT_PUBLIC_API_URL) for consumer services
// communicating with peer infrastructure and application services.
func (e *Engine) resolveInterServiceConnectionEnv(
	execCtx context.Context,
	project *models.Project,
	service *models.Service,
	allProjectServices []*models.Service,
	runtimeEnvMap map[string]string,
	redactor *logging.LogRedactor,
	hasExplicitDatabaseURL bool,
	hasExplicitRedisURL bool,
) []string {
	var svcEnv []string

	// Identify all database and cache services in the project to avoid ambiguous automatic selection
	var dbServices []*models.Service
	var redisServices []*models.Service
	for _, p := range allProjectServices {
		pNameLower := strings.ToLower(p.Name)
		pImageLower := strings.ToLower(p.Image)
		isDB := strings.Contains(pNameLower, "postgres") || strings.Contains(pImageLower, "postgres") ||
			strings.Contains(pNameLower, "mysql") || strings.Contains(pNameLower, "mariadb") || strings.Contains(pImageLower, "mysql") ||
			strings.Contains(pNameLower, "mongo") || strings.Contains(pImageLower, "mongo") ||
			pNameLower == "db" || pNameLower == "database"
		if isDB {
			dbServices = append(dbServices, p)
		}
		isCache := strings.Contains(pNameLower, "redis") || strings.Contains(pImageLower, "redis") ||
			pNameLower == "cache" || pNameLower == "queue" || pNameLower == "valkey"
		if isCache {
			redisServices = append(redisServices, p)
		}
	}
	isSingleDB := len(dbServices) == 1
	isSingleRedis := len(redisServices) == 1

	for _, peer := range allProjectServices {
		peerIntPort := peer.InternalPort
		if peerIntPort <= 0 {
			peerIntPort = 8080
		}
		upperName := strings.ToUpper(strings.ReplaceAll(peer.Name, "-", "_"))
		svcEnv = append(svcEnv, fmt.Sprintf("%s_HOST=%s", upperName, peer.Name))
		svcEnv = append(svcEnv, fmt.Sprintf("%s_PORT=%d", upperName, peerIntPort))

		peerNameLower := strings.ToLower(peer.Name)
		peerImageLower := strings.ToLower(peer.Image)
		isPostgres := strings.Contains(peerNameLower, "postgres") || strings.Contains(peerImageLower, "postgres")
		isRedis := strings.Contains(peerNameLower, "redis") || strings.Contains(peerImageLower, "redis")
		isMySQL := strings.Contains(peerNameLower, "mysql") || strings.Contains(peerNameLower, "mariadb") || strings.Contains(peerImageLower, "mysql")
		isMongo := strings.Contains(peerNameLower, "mongo") || strings.Contains(peerImageLower, "mongo")

		// Query target peer service's own configuration and secrets (not the consumer's environment!)
		var peerEnvMap map[string]string
		if e.secretService != nil {
			if m, _, err := e.secretService.GetDecryptedEnvMap(execCtx, project.ID, &peer.ID, models.EnvScopeRuntime); err == nil && len(m) > 0 {
				peerEnvMap = m
			}
		}
		if peerEnvMap == nil {
			peerEnvMap = make(map[string]string)
		}

		if isRedis {
			redisPass := ""
			redisUser := ""
			if p, ok := peerEnvMap["REDIS_PASSWORD"]; ok && p != "" && p != "[REDACTED]" {
				redisPass = p
			}
			if u, ok := peerEnvMap["REDIS_USER"]; ok && u != "" && u != "[REDACTED]" {
				redisUser = u
			}
			redisURLObj := &url.URL{
				Scheme: "redis",
				Host:   fmt.Sprintf("%s:%d", peer.Name, peerIntPort),
				Path:   "/0",
			}
			if redisPass != "" {
				if redisUser != "" {
					redisURLObj.User = url.UserPassword(redisUser, redisPass)
				} else {
					redisURLObj.User = url.UserPassword("", redisPass)
				}
				redactor.AddSecret(redisPass)
			}
			redisURL := redisURLObj.String()
			redactor.AddSecret(redisURL)

			svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=%s", upperName, redisURL))
			// Unambiguous selection: only set generic REDIS_URL if there is exactly 1 redis service and not explicitly overridden
			if !hasExplicitRedisURL && isSingleRedis {
				svcEnv = append(svcEnv, fmt.Sprintf("REDIS_URL=%s", redisURL))
				hasExplicitRedisURL = true
			}
		} else if isPostgres {
			pgUser := "forgelab"
			pgPass := "forgelab_dev_password"
			pgDB := "forgelab"
			if u, ok := peerEnvMap["POSTGRES_USER"]; ok && u != "" && u != "[REDACTED]" {
				pgUser = u
			}
			if p, ok := peerEnvMap["POSTGRES_PASSWORD"]; ok && p != "" && p != "[REDACTED]" {
				pgPass = p
			}
			if d, ok := peerEnvMap["POSTGRES_DB"]; ok && d != "" && d != "[REDACTED]" {
				pgDB = d
			}
			// Use standard library url.UserPassword to guarantee RFC-compliant percent encoding of credentials
			dbURLObj := &url.URL{
				Scheme:   "postgres",
				User:     url.UserPassword(pgUser, pgPass),
				Host:     fmt.Sprintf("%s:%d", peer.Name, peerIntPort),
				Path:     "/" + pgDB,
				RawQuery: "sslmode=disable",
			}
			dbURL := dbURLObj.String()
			redactor.AddSecret(pgPass)
			redactor.AddSecret(dbURL)

			svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=%s", upperName, dbURL))
			// Unambiguous selection: only set generic DATABASE_URL if there is exactly 1 database and not explicitly overridden
			if !hasExplicitDatabaseURL && isSingleDB {
				svcEnv = append(svcEnv, fmt.Sprintf("DATABASE_URL=%s", dbURL))
				hasExplicitDatabaseURL = true
			}
		} else if isMySQL {
			myUser := "root"
			myPass := "root"
			myDB := "forgelab"
			if u, ok := peerEnvMap["MYSQL_USER"]; ok && u != "" && u != "[REDACTED]" {
				myUser = u
			}
			if p, ok := peerEnvMap["MYSQL_PASSWORD"]; ok && p != "" && p != "[REDACTED]" {
				myPass = p
			} else if p, ok := peerEnvMap["MYSQL_ROOT_PASSWORD"]; ok && p != "" && p != "[REDACTED]" {
				myPass = p
			}
			if d, ok := peerEnvMap["MYSQL_DATABASE"]; ok && d != "" && d != "[REDACTED]" {
				myDB = d
			}
			dbURLObj := &url.URL{
				Scheme: "mysql",
				User:   url.UserPassword(myUser, myPass),
				Host:   fmt.Sprintf("%s:%d", peer.Name, peerIntPort),
				Path:   "/" + myDB,
			}
			dbURL := dbURLObj.String()
			redactor.AddSecret(myPass)
			redactor.AddSecret(dbURL)
			svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=%s", upperName, dbURL))
			if !hasExplicitDatabaseURL && isSingleDB {
				svcEnv = append(svcEnv, fmt.Sprintf("DATABASE_URL=%s", dbURL))
				hasExplicitDatabaseURL = true
			}
		} else if isMongo {
			mUser := ""
			mPass := ""
			mDB := ""
			if u, ok := peerEnvMap["MONGO_INITDB_ROOT_USERNAME"]; ok && u != "" && u != "[REDACTED]" {
				mUser = u
			}
			if p, ok := peerEnvMap["MONGO_INITDB_ROOT_PASSWORD"]; ok && p != "" && p != "[REDACTED]" {
				mPass = p
			}
			if d, ok := peerEnvMap["MONGO_INITDB_DATABASE"]; ok && d != "" && d != "[REDACTED]" {
				mDB = d
			}
			dbURLObj := &url.URL{
				Scheme: "mongodb",
				Host:   fmt.Sprintf("%s:%d", peer.Name, peerIntPort),
				Path:   "/" + mDB,
			}
			if mPass != "" {
				if mUser != "" {
					dbURLObj.User = url.UserPassword(mUser, mPass)
				} else {
					dbURLObj.User = url.UserPassword("", mPass)
				}
				redactor.AddSecret(mPass)
			}
			dbURL := dbURLObj.String()
			redactor.AddSecret(dbURL)
			svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=%s", upperName, dbURL))
			if !hasExplicitDatabaseURL && isSingleDB {
				svcEnv = append(svcEnv, fmt.Sprintf("DATABASE_URL=%s", dbURL))
				hasExplicitDatabaseURL = true
			}
		} else {
			svcEnv = append(svcEnv, fmt.Sprintf("%s_URL=http://%s:%d", upperName, peer.Name, peerIntPort))
		}

		if peer.Role == models.RoleBackend {
			svcEnv = append(svcEnv, fmt.Sprintf("BACKEND_INTERNAL_URL=http://%s:%d", peer.Name, peerIntPort))
			if service.Role == models.RoleFrontend {
				// Browser-side frontend requests MUST NEVER depend on private Docker DNS names!
				browserURL := ""
				if peer.PreviewURL != nil && *peer.PreviewURL != "" {
					browserURL = *peer.PreviewURL
				} else if peer.CustomDomain != nil && *peer.CustomDomain != "" {
					browserURL = fmt.Sprintf("https://%s", *peer.CustomDomain)
				} else if peer.HostPort != nil && *peer.HostPort > 0 {
					host := e.GetPublicHost()
					browserURL = fmt.Sprintf("http://%s:%d", host, *peer.HostPort)
				}

				if browserURL != "" {
					if _, ok := runtimeEnvMap["NEXT_PUBLIC_API_URL"]; !ok {
						svcEnv = append(svcEnv, fmt.Sprintf("NEXT_PUBLIC_API_URL=%s", browserURL))
					}
					if _, ok := runtimeEnvMap["VITE_API_URL"]; !ok {
						svcEnv = append(svcEnv, fmt.Sprintf("VITE_API_URL=%s", browserURL))
					}
					if _, ok := runtimeEnvMap["API_URL"]; !ok {
						svcEnv = append(svcEnv, fmt.Sprintf("API_URL=%s", browserURL))
					}
				}
				// Provide container-to-container backend URL for server-side rendering (SSR)
				if _, ok := runtimeEnvMap["BACKEND_URL"]; !ok {
					svcEnv = append(svcEnv, fmt.Sprintf("BACKEND_URL=http://%s:%d", peer.Name, peerIntPort))
				}
			} else {
				svcEnv = append(svcEnv, fmt.Sprintf("BACKEND_URL=http://%s:%d", peer.Name, peerIntPort))
				svcEnv = append(svcEnv, fmt.Sprintf("API_URL=http://%s:%d", peer.Name, peerIntPort))
			}
		}
	}

	return svcEnv
}
