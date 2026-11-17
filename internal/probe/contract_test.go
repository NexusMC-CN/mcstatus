package probe

import (
	"encoding/json"
	"testing"
	"time"
)

// Losing explicit endpoints would silently revive an obsolete legacy address.
func TestBedrockExplicitEndpointsSurviveRequestAndJob(t *testing.T) {
	const input = `{"platform":"bedrock","bedrock_transport":"auto","host":"obsolete.example","port":19135,"nethernet_endpoint":{"host":"new.example","port":19132,"scheme":"https"},"raknet_endpoint":{"host":"legacy.example","port":19133}}`
	var req Request
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	svc := NewService(1, 1, time.Second, time.Minute)
	job, err := svc.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(job.Request)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bedrock_transport", "nethernet_endpoint", "raknet_endpoint"} {
		if len(got[key]) == 0 {
			t.Errorf("request/job discarded %s: %s", key, data)
		}
	}
	var endpoint struct {
		Host   string
		Port   int
		Scheme string
	}
	if err := json.Unmarshal(got["nethernet_endpoint"], &endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "new.example" || endpoint.Port != 19132 || endpoint.Scheme != "https" {
		t.Fatalf("endpoint changed: %+v", endpoint)
	}
}

// A configured endpoint is sufficient; a legacy host must not be required.
func TestBedrockExplicitEndpointWithoutLegacyHost(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"platform":"bedrock","nethernet_endpoint":{"host":"new.example"}}`), &req); err != nil {
		t.Fatal(err)
	}
	if err := validateRequest(normalizeRequest(req)); err != nil {
		t.Fatalf("valid explicit endpoint rejected: %v", err)
	}
}

// An unknown mode must not silently trigger arbitrary transport attempts.
func TestBedrockTransportRejectsUnknownMode(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"platform":"bedrock","host":"old.example","bedrock_transport":"typo"}`), &req); err != nil {
		t.Fatal(err)
	}
	if err := validateRequest(normalizeRequest(req)); err == nil {
		t.Fatal("unknown bedrock transport accepted")
	}
}

func TestLegacyRequestDefaultPortsRemainCompatible(t *testing.T) {
	for _, tc := range []struct {
		input    string
		platform Platform
		port     int
	}{
		{`{"host":"java.example"}`, PlatformJava, 25565},
		{`{"platform":"bedrock","host":"bedrock.example"}`, PlatformBedrock, 19132},
	} {
		var req Request
		if err := json.Unmarshal([]byte(tc.input), &req); err != nil {
			t.Fatal(err)
		}
		req = normalizeRequest(req)
		if err := validateRequest(req); err != nil {
			t.Fatal(err)
		}
		if req.Platform != tc.platform || req.Port != tc.port {
			t.Fatalf("legacy defaults changed: %+v", req)
		}
	}
}
