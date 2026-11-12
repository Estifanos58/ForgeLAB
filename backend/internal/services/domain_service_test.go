package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/proxy"
)

type stubDNSResolver struct {
	txtRecords map[string][]string
	cnames     map[string]string
	hosts      map[string][]string
}

func (s *stubDNSResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if vals, ok := s.txtRecords[name]; ok {
		return vals, nil
	}
	return nil, errors.New("NXDOMAIN")
}

func (s *stubDNSResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if c, ok := s.cnames[host]; ok {
		return c, nil
	}
	return "", errors.New("no CNAME")
}

func (s *stubDNSResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if h, ok := s.hosts[host]; ok {
		return h, nil
	}
	return nil, errors.New("no A/AAAA records")
}

func TestDomainService_LifecycleAndVerification(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	serviceID := uuid.New()
	hostPort := 19091

	svcSvc := NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(_ context.Context, id uuid.UUID) (*models.Service, error) {
		if id != serviceID {
			return nil, ErrServiceNotFound
		}
		return &models.Service{
			ID:           serviceID,
			ProjectID:    projectID,
			Name:         "web",
			InternalPort: 3000,
			HostPort:     &hostPort,
		}, nil
	})

	router := proxy.NewTrafficRouter()
	var syncedPayload string
	var syncCalls int
	caddySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		syncCalls++
		b, _ := io.ReadAll(r.Body)
		syncedPayload = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer caddySrv.Close()

	syncer := proxy.NewCaddySyncer(caddySrv.URL, "tls@example.com", caddySrv.Client())
	svc := NewDomainService(nil, svcSvc, router, syncer, "edge.forgelab.dev", "127.0.0.1")

	resolver := &stubDNSResolver{
		txtRecords: make(map[string][]string),
		cnames:     make(map[string]string),
		hosts:      make(map[string][]string),
	}
	svc.SetDNSResolver(resolver)

	project := &models.Project{
		ID:           projectID,
		OwnerID:      uuid.New(),
		Name:         "demo",
		InternalPort: 3000,
	}

	// Reject reserved / invalid domains
	_, err := svc.AddDomain(ctx, project, "myapp.local", &serviceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidationFailed)

	// Reject service belonging to another project
	otherSvcID := uuid.New()
	svcSvc.SetGetServiceForTest(func(_ context.Context, id uuid.UUID) (*models.Service, error) {
		if id == otherSvcID {
			return &models.Service{ID: otherSvcID, ProjectID: uuid.New(), Name: "foreign"}, nil
		}
		return &models.Service{ID: serviceID, ProjectID: projectID, Name: "web", InternalPort: 3000, HostPort: &hostPort}, nil
	})
	_, err = svc.AddDomain(ctx, project, "app.example.com", &otherSvcID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidationFailed)

	// Register valid domain
	dom, err := svc.AddDomain(ctx, project, "https://App.Example.com/", &serviceID)
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", dom.Domain)
	assert.False(t, dom.Verified)
	assert.Equal(t, models.DomainTLSStatusPending, dom.TLSStatus)
	assert.Equal(t, hostPort, dom.TargetPort)
	assert.True(t, strings.HasPrefix(dom.VerificationToken, "fl-verify-"))
	assert.Equal(t, "_forgelab-verify.app.example.com", dom.VerificationRecordName)

	// Duplicate domain rejected cleanly
	_, err = svc.AddDomain(ctx, project, "app.example.com", &serviceID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDomainExists)

	// Verification fails if TXT record is absent
	failedDom, err := svc.VerifyDomain(ctx, projectID, dom.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDomainVerification)
	assert.False(t, failedDom.Verified)
	assert.Equal(t, models.DomainTLSStatusError, failedDom.TLSStatus)
	assert.Equal(t, 0, syncCalls)

	// Verification fails if TXT matches but CNAME/A does not point to ingress host
	resolver.txtRecords["_forgelab-verify.app.example.com"] = []string{dom.VerificationToken}
	_, err = svc.VerifyDomain(ctx, projectID, dom.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDomainVerification)

	// Blue-Green switch updates TrafficRouter host port, then DNS routing matches -> Caddy sync uses live port
	_, err = router.SwitchTraffic(serviceID, projectID, "web", "127.0.0.1", 19099, "green-cid")
	require.NoError(t, err)
	resolver.cnames["app.example.com"] = "edge.forgelab.dev."

	verifiedDom, err := svc.VerifyDomain(ctx, projectID, dom.ID)
	require.NoError(t, err)
	assert.True(t, verifiedDom.Verified)
	assert.Equal(t, models.DomainTLSStatusActive, verifiedDom.TLSStatus)
	assert.Equal(t, 19099, verifiedDom.TargetPort)
	assert.Equal(t, 1, syncCalls)
	assert.Contains(t, syncedPayload, `"app.example.com"`)
	assert.Contains(t, syncedPayload, `"127.0.0.1:19099"`)

	// List domains returns verified record
	listed, err := svc.ListDomains(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, listed[0].Verified)

	// Remove domain removes it from DB and re-syncs empty route list to Caddy
	require.NoError(t, svc.RemoveDomain(ctx, projectID, dom.ID))
	assert.Equal(t, 2, syncCalls)

	var caddyCfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(syncedPayload), &caddyCfg))
	assert.NotContains(t, syncedPayload, `"app.example.com"`)

	listedAfter, err := svc.ListDomains(ctx, projectID)
	require.NoError(t, err)
	assert.Empty(t, listedAfter)
}

func TestDomainService_NeverReportsHTTPSActiveUnlessCaddyConfiguredAndSynced(t *testing.T) {
	ctx := context.Background()
	projectID := uuid.New()
	serviceID := uuid.New()
	hostPort := 19123

	svcSvc := NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(_ context.Context, id uuid.UUID) (*models.Service, error) {
		return &models.Service{
			ID:           serviceID,
			ProjectID:    projectID,
			Name:         "web",
			InternalPort: 3000,
			HostPort:     &hostPort,
		}, nil
	})
	project := &models.Project{ID: projectID, OwnerID: uuid.New(), Name: "demo", InternalPort: 3000}

	// 1. Unconfigured Caddy syncer: DNS TXT passes, but domain MUST NOT report tls_status=active or https_active=true
	unconfiguredSvc := NewDomainService(nil, svcSvc, proxy.NewTrafficRouter(), proxy.NewCaddySyncer("", "", nil), "", "127.0.0.1")
	resolver := &stubDNSResolver{
		txtRecords: make(map[string][]string),
		cnames:     make(map[string]string),
		hosts:      make(map[string][]string),
	}
	unconfiguredSvc.SetDNSResolver(resolver)

	dom, err := unconfiguredSvc.AddDomain(ctx, project, "nocaddy.example.org", &serviceID)
	require.NoError(t, err)
	resolver.txtRecords[dom.VerificationRecordName] = []string{dom.VerificationToken}

	verifiedNoCaddy, err := unconfiguredSvc.VerifyDomain(ctx, projectID, dom.ID)
	require.NoError(t, err)
	assert.True(t, verifiedNoCaddy.Verified)
	assert.Equal(t, models.DomainTLSStatusProxyUnconfigured, verifiedNoCaddy.TLSStatus)
	assert.False(t, verifiedNoCaddy.HTTPSActive, "HTTPSActive MUST be false when Caddy is unconfigured")
	require.NotNil(t, verifiedNoCaddy.LastError)
	assert.Contains(t, *verifiedNoCaddy.LastError, "CADDY_ADMIN_URL")

	// ListDomains & GetDomain must preserve non-active HTTPS truth
	fetched, err := unconfiguredSvc.GetDomain(ctx, projectID, dom.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DomainTLSStatusProxyUnconfigured, fetched.TLSStatus)
	assert.False(t, fetched.HTTPSActive)

	// 2. Configured Caddy syncer failing HTTP /load MUST NOT report active
	failingCaddy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "caddy reload rejected", http.StatusBadGateway)
	}))
	defer failingCaddy.Close()

	failingSvc := NewDomainService(
		nil,
		svcSvc,
		proxy.NewTrafficRouter(),
		proxy.NewCaddySyncer(failingCaddy.URL, "", failingCaddy.Client()),
		"edge.forgelab.dev",
		"127.0.0.1",
	)
	failingResolver := &stubDNSResolver{
		txtRecords: make(map[string][]string),
		cnames:     map[string]string{"failing.example.org": "edge.forgelab.dev"},
		hosts:      make(map[string][]string),
	}
	failingSvc.SetDNSResolver(failingResolver)

	failingDom, err := failingSvc.AddDomain(ctx, project, "failing.example.org", &serviceID)
	require.NoError(t, err)
	failingResolver.txtRecords[failingDom.VerificationRecordName] = []string{failingDom.VerificationToken}

	afterFailDom, err := failingSvc.VerifyDomain(ctx, projectID, failingDom.ID)
	require.Error(t, err)
	assert.True(t, afterFailDom.Verified)
	assert.Equal(t, models.DomainTLSStatusError, afterFailDom.TLSStatus)
	assert.False(t, afterFailDom.HTTPSActive, "HTTPSActive MUST be false when Caddy provisioning fails")
	assert.NotEqual(t, models.DomainTLSStatusActive, afterFailDom.TLSStatus)
}
