package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/proxy"
)

var (
	ErrDomainNotFound     = errors.New("custom domain not found")
	ErrDomainExists       = errors.New("custom domain is already registered")
	ErrDomainVerification = errors.New("custom domain DNS verification failed")
	ErrCaddyNotConfigured = errors.New("Caddy reverse proxy is not configured")
)

// DNSResolver abstracts DNS TXT/CNAME/A lookups for testability.
type DNSResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DomainService manages custom domain bindings, DNS ownership verification, and Caddy HTTPS route sync.
type DomainService struct {
	db             *pgxpool.Pool
	serviceService *ServiceService
	projectService *ProjectService
	trafficRouter  *proxy.TrafficRouter
	caddySyncer    *proxy.CaddySyncer
	resolver       DNSResolver
	ingressHost    string
	targetHost     string

	memMu      sync.RWMutex
	memDomains map[uuid.UUID]*models.CustomDomain
}

// NewDomainService creates a new DomainService.
func NewDomainService(
	db *pgxpool.Pool,
	serviceSvc *ServiceService,
	trafficRouter *proxy.TrafficRouter,
	caddySyncer *proxy.CaddySyncer,
	ingressHost string,
	targetHost string,
) *DomainService {
	if strings.TrimSpace(targetHost) == "" {
		targetHost = "127.0.0.1"
	}
	return &DomainService{
		db:             db,
		serviceService: serviceSvc,
		trafficRouter:  trafficRouter,
		caddySyncer:    caddySyncer,
		resolver:       net.DefaultResolver,
		ingressHost:    strings.TrimSpace(ingressHost),
		targetHost:     strings.TrimSpace(targetHost),
		memDomains:     make(map[uuid.UUID]*models.CustomDomain),
	}
}

// SetDNSResolver overrides the DNS resolver (used in unit tests).
func (s *DomainService) SetDNSResolver(resolver DNSResolver) {
	if resolver != nil {
		s.resolver = resolver
	}
}

// SetProjectService configures the project service for target port fallback resolution.
func (s *DomainService) SetProjectService(projectSvc *ProjectService) {
	s.projectService = projectSvc
}

// IsCaddyConfigured returns true when a Caddy Admin API endpoint is configured for TLS/route provisioning.
func (s *DomainService) IsCaddyConfigured() bool {
	return s != nil && s.caddySyncer != nil && s.caddySyncer.IsConfigured()
}

func (s *DomainService) populateEffectiveState(d *models.CustomDomain) {
	if d == nil {
		return
	}
	if !s.IsCaddyConfigured() && d.Verified && d.TLSStatus == models.DomainTLSStatusActive {
		d.TLSStatus = models.DomainTLSStatusProxyUnconfigured
		if d.LastError == nil || strings.TrimSpace(*d.LastError) == "" {
			msg := "DNS verified, but HTTPS routing is inactive because CADDY_ADMIN_URL is not configured"
			d.LastError = &msg
		}
	}
	d.PopulateDNSInstructions(s.ingressHost)
}

func generateDomainVerificationToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate DNS verification token: %w", err)
	}
	return "fl-verify-" + hex.EncodeToString(buf), nil
}

func isLocalIngressHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "" || h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func normalizeDNSName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func (s *DomainService) resolveTargetPort(ctx context.Context, project *models.Project, serviceID *uuid.UUID) (int, *uuid.UUID, error) {
	if serviceID != nil && *serviceID != uuid.Nil {
		if s.serviceService != nil {
			svc, err := s.serviceService.GetService(ctx, *serviceID)
			if err != nil {
				return 0, nil, err
			}
			if project != nil && svc.ProjectID != project.ID {
				return 0, nil, fmt.Errorf("%w: target service does not belong to this project", ErrValidationFailed)
			}
			if s.trafficRouter != nil {
				if rt, ok := s.trafficRouter.GetRoute(svc.ID); ok && rt.HostPort > 0 {
					return rt.HostPort, &svc.ID, nil
				}
			}
			if svc.HostPort != nil && *svc.HostPort > 0 {
				return *svc.HostPort, &svc.ID, nil
			}
			if svc.InternalPort > 0 {
				return svc.InternalPort, &svc.ID, nil
			}
		}
		return 8080, serviceID, nil
	}

	if project != nil && s.serviceService != nil {
		svcs, err := s.serviceService.ListServices(ctx, project.ID)
		if err == nil && len(svcs) > 0 {
			var chosen *models.Service
			for _, svc := range svcs {
				if svc.Role == models.RoleFrontend || svc.PublicExposed {
					chosen = svc
					break
				}
			}
			if chosen == nil {
				chosen = svcs[0]
			}
			if chosen != nil {
				if s.trafficRouter != nil {
					if rt, ok := s.trafficRouter.GetRoute(chosen.ID); ok && rt.HostPort > 0 {
						return rt.HostPort, &chosen.ID, nil
					}
				}
				if chosen.HostPort != nil && *chosen.HostPort > 0 {
					return *chosen.HostPort, &chosen.ID, nil
				}
				if chosen.InternalPort > 0 {
					return chosen.InternalPort, &chosen.ID, nil
				}
			}
		}
	}

	if project != nil {
		if project.Port != nil && *project.Port > 0 {
			return *project.Port, nil, nil
		}
		if project.InternalPort > 0 {
			return project.InternalPort, nil, nil
		}
	}
	return 8080, nil, nil
}

// AddDomain registers a new pending custom domain binding for a project/service.
func (s *DomainService) AddDomain(ctx context.Context, project *models.Project, rawDomain string, serviceID *uuid.UUID) (*models.CustomDomain, error) {
	if project == nil || project.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: valid project is required", ErrValidationFailed)
	}

	normalized, err := proxy.ValidateCustomDomain(rawDomain)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidationFailed, err)
	}

	targetPort, resolvedServiceID, err := s.resolveTargetPort(ctx, project, serviceID)
	if err != nil {
		return nil, err
	}

	token, err := generateDomainVerificationToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	record := &models.CustomDomain{
		ID:                uuid.New(),
		ProjectID:         project.ID,
		ServiceID:         resolvedServiceID,
		Domain:            normalized,
		VerificationToken: token,
		Verified:          false,
		TLSStatus:         models.DomainTLSStatusPending,
		TargetPort:        targetPort,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	record.PopulateDNSInstructions(s.ingressHost)

	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		for _, existing := range s.memDomains {
			if existing.Domain == normalized {
				return nil, ErrDomainExists
			}
		}
		cp := *record
		s.memDomains[cp.ID] = &cp
		return record, nil
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO custom_domains (
			id, project_id, service_id, domain, verification_token, verified,
			tls_status, target_port, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		record.ID, record.ProjectID, record.ServiceID, record.Domain,
		record.VerificationToken, record.Verified, record.TLSStatus,
		record.TargetPort, record.CreatedAt, record.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDomainExists
		}
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return nil, ErrDomainExists
		}
		return nil, fmt.Errorf("failed to register custom domain: %w", err)
	}

	return record, nil
}

// ListDomains returns all custom domains configured for a project.
func (s *DomainService) ListDomains(ctx context.Context, projectID uuid.UUID) ([]*models.CustomDomain, error) {
	if s.db == nil {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		list := make([]*models.CustomDomain, 0)
		for _, d := range s.memDomains {
			if d.ProjectID == projectID {
				cp := *d
				s.populateEffectiveState(&cp)
				list = append(list, &cp)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		})
		return list, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, service_id, domain, verification_token, verified,
		        tls_status, target_port, last_checked_at, last_error, created_at, updated_at
		 FROM custom_domains
		 WHERE project_id = $1
		 ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list custom domains: %w", err)
	}
	defer rows.Close()

	domains := make([]*models.CustomDomain, 0)
	for rows.Next() {
		d := &models.CustomDomain{}
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.ServiceID, &d.Domain, &d.VerificationToken,
			&d.Verified, &d.TLSStatus, &d.TargetPort, &d.LastCheckedAt,
			&d.LastError, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan custom domain: %w", err)
		}
		s.populateEffectiveState(d)
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

// GetDomain fetches a single custom domain belonging to a project.
func (s *DomainService) GetDomain(ctx context.Context, projectID, domainID uuid.UUID) (*models.CustomDomain, error) {
	if s.db == nil {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		d, ok := s.memDomains[domainID]
		if !ok || d.ProjectID != projectID {
			return nil, ErrDomainNotFound
		}
		cp := *d
		s.populateEffectiveState(&cp)
		return &cp, nil
	}

	d := &models.CustomDomain{}
	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, service_id, domain, verification_token, verified,
		        tls_status, target_port, last_checked_at, last_error, created_at, updated_at
		 FROM custom_domains
		 WHERE id = $1 AND project_id = $2`,
		domainID, projectID,
	).Scan(
		&d.ID, &d.ProjectID, &d.ServiceID, &d.Domain, &d.VerificationToken,
		&d.Verified, &d.TLSStatus, &d.TargetPort, &d.LastCheckedAt,
		&d.LastError, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDomainNotFound
		}
		return nil, fmt.Errorf("failed to load custom domain: %w", err)
	}
	s.populateEffectiveState(d)
	return d, nil
}

// RemoveDomain deletes a custom domain from a project and updates Caddy routes.
func (s *DomainService) RemoveDomain(ctx context.Context, projectID, domainID uuid.UUID) error {
	if s.db == nil {
		s.memMu.Lock()
		d, ok := s.memDomains[domainID]
		if !ok || d.ProjectID != projectID {
			s.memMu.Unlock()
			return ErrDomainNotFound
		}
		delete(s.memDomains, domainID)
		s.memMu.Unlock()
	} else {
		tag, err := s.db.Exec(ctx, "DELETE FROM custom_domains WHERE id = $1 AND project_id = $2", domainID, projectID)
		if err != nil {
			return fmt.Errorf("failed to delete custom domain: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrDomainNotFound
		}
	}

	if err := s.SyncAllVerifiedRoutes(ctx); err != nil {
		slog.Warn("failed to synchronize Caddy routes after domain removal", "domain_id", domainID, "error", err)
	}
	return nil
}

func (s *DomainService) persistDomainState(ctx context.Context, d *models.CustomDomain) error {
	d.UpdatedAt = time.Now().UTC()
	if s.db == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		cp := *d
		s.memDomains[d.ID] = &cp
		return nil
	}

	_, err := s.db.Exec(ctx,
		`UPDATE custom_domains
		 SET verified = $1, tls_status = $2, target_port = $3,
		     last_checked_at = $4, last_error = $5, updated_at = $6
		 WHERE id = $7 AND project_id = $8`,
		d.Verified, d.TLSStatus, d.TargetPort, d.LastCheckedAt, d.LastError, d.UpdatedAt, d.ID, d.ProjectID,
	)
	return err
}

// VerifyDomain performs DNS TXT ownership verification (and routing verification when a non-local ingress host
// is configured), then updates Caddy TLS routing if verified.
func (s *DomainService) VerifyDomain(ctx context.Context, projectID, domainID uuid.UUID) (*models.CustomDomain, error) {
	d, err := s.GetDomain(ctx, projectID, domainID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	d.LastCheckedAt = &now

	recordName := "_forgelab-verify." + d.Domain
	txtValues, txtErr := s.resolver.LookupTXT(ctx, recordName)
	txtMatched := false
	if txtErr == nil {
		for _, val := range txtValues {
			if strings.TrimSpace(val) == d.VerificationToken {
				txtMatched = true
				break
			}
		}
	}

	if !txtMatched {
		msg := fmt.Sprintf("missing required TXT record %s = %s", recordName, d.VerificationToken)
		if txtErr != nil {
			msg = fmt.Sprintf("%s (DNS lookup error: %v)", msg, txtErr)
		}
		d.Verified = false
		d.TLSStatus = models.DomainTLSStatusError
		d.LastError = &msg
		_ = s.persistDomainState(ctx, d)
		d.PopulateDNSInstructions(s.ingressHost)
		return d, fmt.Errorf("%w: %s", ErrDomainVerification, msg)
	}

	// Verify routing (CNAME or matching host IP) when an explicit non-local ingress host is configured
	if !isLocalIngressHost(s.ingressHost) {
		routed := false
		expectedHost := normalizeDNSName(s.ingressHost)
		if cname, err := s.resolver.LookupCNAME(ctx, d.Domain); err == nil && normalizeDNSName(cname) == expectedHost {
			routed = true
		}
		if !routed {
			if domIPs, dErr := s.resolver.LookupHost(ctx, d.Domain); dErr == nil {
				if ingIPs, iErr := s.resolver.LookupHost(ctx, s.ingressHost); iErr == nil {
					ingSet := make(map[string]struct{}, len(ingIPs))
					for _, ip := range ingIPs {
						ingSet[ip] = struct{}{}
					}
					for _, dip := range domIPs {
						if _, ok := ingSet[dip]; ok {
							routed = true
							break
						}
					}
				}
			}
		}
		if !routed {
			msg := fmt.Sprintf("domain %s does not resolve to ingress host %s via CNAME or A/AAAA record", d.Domain, s.ingressHost)
			d.Verified = false
			d.TLSStatus = models.DomainTLSStatusError
			d.LastError = &msg
			_ = s.persistDomainState(ctx, d)
			d.PopulateDNSInstructions(s.ingressHost)
			return d, fmt.Errorf("%w: %s", ErrDomainVerification, msg)
		}
	}

	// Refresh live target port if service or traffic router has a newer active port
	if d.ServiceID != nil {
		if port, _, portErr := s.resolveTargetPort(ctx, nil, d.ServiceID); portErr == nil && port > 0 {
			d.TargetPort = port
		}
	}

	d.Verified = true

	// Never report HTTPS as active unless Caddy Admin API is configured AND route synchronization succeeds.
	if !s.IsCaddyConfigured() {
		msg := "DNS verified, but HTTPS routing is inactive because CADDY_ADMIN_URL is not configured"
		d.TLSStatus = models.DomainTLSStatusProxyUnconfigured
		d.LastError = &msg
		if err := s.persistDomainState(ctx, d); err != nil {
			return nil, fmt.Errorf("failed to persist verified domain status: %w", err)
		}
		s.populateEffectiveState(d)
		return d, nil
	}

	d.TLSStatus = models.DomainTLSStatusPending
	d.LastError = nil
	if err := s.persistDomainState(ctx, d); err != nil {
		return nil, fmt.Errorf("failed to persist verified domain status: %w", err)
	}

	if err := s.SyncAllVerifiedRoutes(ctx); err != nil {
		msg := fmt.Sprintf("DNS verified, but Caddy HTTPS route sync failed: %v", err)
		d.TLSStatus = models.DomainTLSStatusError
		d.LastError = &msg
		_ = s.persistDomainState(ctx, d)
		s.populateEffectiveState(d)
		return d, fmt.Errorf("failed to provision HTTPS route in Caddy: %w", err)
	}

	d.TLSStatus = models.DomainTLSStatusActive
	d.LastError = nil
	if err := s.persistDomainState(ctx, d); err != nil {
		return nil, fmt.Errorf("failed to persist active HTTPS domain status: %w", err)
	}

	s.populateEffectiveState(d)
	return d, nil
}

// SyncAllVerifiedRoutes builds ProxyRoute entries for all verified domains and pushes them to Caddy.
func (s *DomainService) SyncAllVerifiedRoutes(ctx context.Context) error {
	if s.caddySyncer == nil || !s.caddySyncer.IsConfigured() {
		return nil
	}

	verifiedDomains, err := s.listAllVerified(ctx)
	if err != nil {
		return err
	}

	routes := make([]proxy.ProxyRoute, 0, len(verifiedDomains))
	for _, d := range verifiedDomains {
		port := d.TargetPort
		if d.ServiceID != nil {
			if s.trafficRouter != nil {
				if rt, ok := s.trafficRouter.GetRoute(*d.ServiceID); ok && rt.HostPort > 0 {
					port = rt.HostPort
				}
			} else if s.serviceService != nil {
				if svc, err := s.serviceService.GetService(ctx, *d.ServiceID); err == nil && svc.HostPort != nil && *svc.HostPort > 0 {
					port = *svc.HostPort
				}
			}
		}
		if port <= 0 {
			port = 8080
		}
		routes = append(routes, proxy.ProxyRoute{
			Domain:     d.Domain,
			TargetHost: s.targetHost,
			TargetPort: port,
			TLSEnabled: true,
			ProjectID:  d.ProjectID.String(),
		})
	}

	return s.caddySyncer.SyncRoutes(ctx, routes)
}

func (s *DomainService) listAllVerified(ctx context.Context) ([]*models.CustomDomain, error) {
	if s.db == nil {
		s.memMu.RLock()
		defer s.memMu.RUnlock()
		res := make([]*models.CustomDomain, 0)
		for _, d := range s.memDomains {
			if d.Verified {
				cp := *d
				res = append(res, &cp)
			}
		}
		return res, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, service_id, domain, verification_token, verified,
		        tls_status, target_port, last_checked_at, last_error, created_at, updated_at
		 FROM custom_domains
		 WHERE verified = TRUE
		 ORDER BY domain ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list verified custom domains: %w", err)
	}
	defer rows.Close()

	res := make([]*models.CustomDomain, 0)
	for rows.Next() {
		d := &models.CustomDomain{}
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.ServiceID, &d.Domain, &d.VerificationToken,
			&d.Verified, &d.TLSStatus, &d.TargetPort, &d.LastCheckedAt,
			&d.LastError, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan verified domain: %w", err)
		}
		res = append(res, d)
	}
	return res, rows.Err()
}
