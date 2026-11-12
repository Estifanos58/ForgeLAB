package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/forgelab/backend/internal/handlers"
	"github.com/forgelab/backend/internal/models"
	"github.com/forgelab/backend/internal/proxy"
	"github.com/forgelab/backend/internal/services"
)

type fakeHandlerDNSResolver struct {
	txt map[string][]string
}

func (f *fakeHandlerDNSResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeHandlerDNSResolver) LookupCNAME(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeHandlerDNSResolver) LookupHost(_ context.Context, _ string) ([]string, error) {
	return []string{"127.0.0.1"}, nil
}

func TestDomainHandler_CRUDAndVerify(t *testing.T) {
	ownerID := uuid.New()
	otherID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	hostPort := 18080

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pID, uID uuid.UUID) (*models.Project, error) {
		if pID != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uID != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{
			ID:           projectID,
			OwnerID:      ownerID,
			Name:         "domains-demo",
			InternalPort: 3000,
			Port:         &hostPort,
		}, nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(_ context.Context, sID uuid.UUID) (*models.Service, error) {
		if sID != serviceID {
			return nil, services.ErrServiceNotFound
		}
		return &models.Service{
			ID:           serviceID,
			ProjectID:    projectID,
			Name:         "web",
			InternalPort: 3000,
			HostPort:     &hostPort,
		}, nil
	})

	var caddySynced bool
	caddySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caddySynced = true
		w.WriteHeader(http.StatusOK)
	}))
	defer caddySrv.Close()

	domSvc := services.NewDomainService(
		nil,
		svcSvc,
		proxy.NewTrafficRouter(),
		proxy.NewCaddySyncer(caddySrv.URL, "", caddySrv.Client()),
		"",
		"127.0.0.1",
	)
	resolver := &fakeHandlerDNSResolver{txt: make(map[string][]string)}
	domSvc.SetDNSResolver(resolver)

	h := handlers.NewDomainHandler(domSvc, projSvc)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if uidStr := req.Header.Get("X-Test-User"); uidStr != "" {
				uid, _ := uuid.Parse(uidStr)
				req = req.WithContext(withTestUser(req.Context(), uid))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/projects/{id}/domains", h.List)
	r.Post("/api/projects/{id}/domains", h.Add)
	r.Delete("/api/projects/{id}/domains/{domainId}", h.Remove)
	r.Post("/api/projects/{id}/domains/{domainId}/verify", h.Verify)

	// 1. Unauthorized without user
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/domains", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// 2. Forbidden for non-owner
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/domains", nil)
	req.Header.Set("X-Test-User", otherID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// 3. Invalid domain rejected (400)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/domains", bytes.NewBufferString(`{"domain":"127.0.0.1"}`))
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 4. Add valid domain (201)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/domains", bytes.NewBufferString(`{"domain":"portal.example.org","service_id":"`+serviceID.String()+`"}`))
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var created models.CustomDomain
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "portal.example.org", created.Domain)
	assert.Equal(t, hostPort, created.TargetPort)

	// 5. Verify without TXT -> 422
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/domains/"+created.ID.String()+"/verify", nil)
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.False(t, caddySynced)

	// 6. Verify with matching TXT -> 200 & Caddy synced
	resolver.txt[created.VerificationRecordName] = []string{created.VerificationToken}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/domains/"+created.ID.String()+"/verify", nil)
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, caddySynced)

	var verified models.CustomDomain
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &verified))
	assert.True(t, verified.Verified)
	assert.Equal(t, models.DomainTLSStatusActive, verified.TLSStatus)
	assert.True(t, verified.HTTPSActive)

	// 7. Delete domain -> 200
	req = httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID.String()+"/domains/"+created.ID.String(), nil)
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDomainHandler_VerifyWithoutCaddyDoesNotReportHTTPSActive(t *testing.T) {
	ownerID := uuid.New()
	projectID := uuid.New()
	serviceID := uuid.New()
	hostPort := 18081

	projSvc := services.NewProjectService(nil, nil, nil, nil)
	projSvc.SetProjectGetterForTest(func(_ context.Context, pID, uID uuid.UUID) (*models.Project, error) {
		if pID != projectID {
			return nil, services.ErrProjectNotFound
		}
		if uID != ownerID {
			return nil, services.ErrProjectNotOwned
		}
		return &models.Project{
			ID:           projectID,
			OwnerID:      ownerID,
			Name:         "domains-no-caddy",
			InternalPort: 3000,
			Port:         &hostPort,
		}, nil
	})

	svcSvc := services.NewServiceService(nil)
	svcSvc.SetGetServiceForTest(func(_ context.Context, sID uuid.UUID) (*models.Service, error) {
		return &models.Service{
			ID:           serviceID,
			ProjectID:    projectID,
			Name:         "web",
			InternalPort: 3000,
			HostPort:     &hostPort,
		}, nil
	})

	// Caddy Admin URL intentionally empty -> Caddy is unconfigured.
	domSvc := services.NewDomainService(
		nil,
		svcSvc,
		proxy.NewTrafficRouter(),
		proxy.NewCaddySyncer("", "", nil),
		"",
		"127.0.0.1",
	)
	resolver := &fakeHandlerDNSResolver{txt: make(map[string][]string)}
	domSvc.SetDNSResolver(resolver)

	h := handlers.NewDomainHandler(domSvc, projSvc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if uidStr := req.Header.Get("X-Test-User"); uidStr != "" {
				uid, _ := uuid.Parse(uidStr)
				req = req.WithContext(withTestUser(req.Context(), uid))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/projects/{id}/domains", h.List)
	r.Post("/api/projects/{id}/domains", h.Add)
	r.Post("/api/projects/{id}/domains/{domainId}/verify", h.Verify)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/domains", bytes.NewBufferString(`{"domain":"nossl.example.org","service_id":"`+serviceID.String()+`"}`))
	req.Header.Set("X-Test-User", ownerID.String())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var created models.CustomDomain
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.False(t, created.HTTPSActive)

	resolver.txt[created.VerificationRecordName] = []string{created.VerificationToken}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID.String()+"/domains/"+created.ID.String()+"/verify", nil)
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var verified models.CustomDomain
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &verified))
	assert.True(t, verified.Verified)
	assert.Equal(t, models.DomainTLSStatusProxyUnconfigured, verified.TLSStatus)
	assert.False(t, verified.HTTPSActive)

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID.String()+"/domains", nil)
	req.Header.Set("X-Test-User", ownerID.String())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var listed []models.CustomDomain
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, models.DomainTLSStatusProxyUnconfigured, listed[0].TLSStatus)
	assert.False(t, listed[0].HTTPSActive)
}
