package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blockbridge/avmcbbs/apps/mcstatus/internal/probe"
)

const explicitProbe = `{"platform":"bedrock","host":"127.0.0.1","bedrock_transport":"auto","nethernet_endpoint":{"host":"nether.example","scheme":"https"},"raknet_endpoint":{"host":"rak.example","port":19133}}`

func TestOptionalInternalBearerProtectsEveryProbeRoute(t *testing.T) {
	service := probe.NewService(1, 2, time.Second, time.Minute)
	defer service.Close()
	token := strings.Repeat("a", 32)
	handler := NewRouter(service, token)
	for _, path := range []string{"/v1/probe", "/v1/probe/jobs", "/v1/probe/jobs/unknown"} {
		for _, authorization := range []string{"", "Basic " + token, "Bearer wrong", "Bearer " + token + " "} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("path=%s authorization=%q status=%d", path, authorization, response.Code)
			}
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/probe/jobs/unknown", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("valid key status=%d", response.Code)
	}
	request.Header.Add("Authorization", "Bearer "+token)
	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, request)
	if duplicate.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate credentials status=%d", duplicate.Code)
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatal(health.Code)
	}
}

func TestProbeJobsRejectInvalidRequestsBeforeQueueing(t *testing.T) {
	for _, body := range []string{
		`{"platform":"invalid","host":"public.example"}`,
		`{"platform":"bedrock","host":"old.example","bedrock_transport":"invalid"}`,
		`{"platform":"bedrock","nethernet_endpoint":{}}`,
		`{"platform":"bedrock","nethernet_endpoint":{"host":"valid.example","scheme":"ftp"}}`,
		`null`,
	} {
		service := probe.NewService(1, 2, time.Second, time.Minute)
		handler := NewRouter(service)
		request := httptest.NewRequest(http.MethodPost, "/v1/probe/jobs", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		service.Close()
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid request accepted: status=%d body=%s", response.Code, body)
		}
	}
}

func TestProbeRequestBodyBoundAndSingleDocument(t *testing.T) {
	oversized := `{"platform":"bedrock","host":"valid.example","padding":"` + strings.Repeat("x", 40*1024) + `"}`
	for _, tc := range []struct {
		body   string
		status int
	}{
		{explicitProbe + "{}", http.StatusBadRequest},
		{explicitProbe + " true", http.StatusBadRequest},
		{oversized, http.StatusRequestEntityTooLarge},
	} {
		service := probe.NewService(1, 2, time.Second, time.Minute)
		handler := NewRouter(service)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/probe/jobs", strings.NewReader(tc.body)))
		service.Close()
		if response.Code != tc.status {
			t.Errorf("body boundary failed: got=%d want=%d", response.Code, tc.status)
		}
	}
}

func TestAsyncProbePreservesCompatibleAndNewFields(t *testing.T) {
	service := probe.NewService(1, 2, time.Second, time.Minute)
	defer service.Close()
	handler := NewRouter(service)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/probe/jobs", strings.NewReader(explicitProbe)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var job probe.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Request.Host != "127.0.0.1" || job.Request.BedrockTransport != "auto" || job.Request.NetherNetEndpoint == nil || job.Request.NetherNetEndpoint.Host != "nether.example" || job.Request.RakNetEndpoint == nil || job.Request.RakNetEndpoint.Port != 19133 {
		t.Fatalf("request fields changed: %+v", job.Request)
	}
	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/v1/probe/jobs/"+job.ID, nil))
	if detail.Code != http.StatusOK {
		t.Fatal(detail.Code)
	}
}

func TestClosedServiceRejectsNewHTTPAdmission(t *testing.T) {
	service := probe.NewService(1, 2, time.Second, time.Minute)
	service.Close()
	handler := NewRouter(service)
	for _, path := range []string{"/v1/probe", "/v1/probe/jobs"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(explicitProbe)))
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("closed service status=%d", response.Code)
		}
	}
}
