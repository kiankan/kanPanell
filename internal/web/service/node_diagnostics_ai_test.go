package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnrichNodeDiagnosisSendsOnlySanitizedEvidence(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("unexpected authorization header %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"explanation\":\"Inbound counts differ\",\"recommendations\":[\"Review sync mode\"]}"}}]}`)
	}))
	defer server.Close()
	t.Setenv("NODE_DIAGNOSTICS_AI_BASE_URL", server.URL+"/v1")
	t.Setenv("NODE_DIAGNOSTICS_AI_API_KEY", "test-secret")
	t.Setenv("NODE_DIAGNOSTICS_AI_MODEL", "test-model")

	diagnosis := &NodeDiagnosis{
		NodeName: "private-node-name",
		Status: "online",
		XrayState: "error",
		XrayError: "private raw error containing a token",
		LocalInboundCount: 4,
		RemoteInboundCount: 2,
		Findings: []NodeDiagnosticFinding{{Code: "inbound_count_mismatch", Severity: "warning", Message: "must not be sent"}},
	}
	enrichNodeDiagnosis(context.Background(), diagnosis)
	if !diagnosis.AIEnabled || diagnosis.AIExplanation != "Inbound counts differ" {
		t.Fatalf("expected AI analysis, got %#v", diagnosis)
	}
	encoded, err := json.Marshal(received)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"private-node-name", "private raw error", "must not be sent", "test-secret"} {
		if strings.Contains(payload, forbidden) {
			t.Errorf("AI request unexpectedly included sensitive/raw value %q", forbidden)
		}
	}
	if !strings.Contains(payload, "inbound_count_mismatch") {
		t.Errorf("expected sanitized finding code in payload: %s", payload)
	}
}

func TestEnrichNodeDiagnosisSkipsWhenProviderIsNotConfigured(t *testing.T) {
	t.Setenv("NODE_DIAGNOSTICS_AI_BASE_URL", "")
	t.Setenv("NODE_DIAGNOSTICS_AI_API_KEY", "")
	t.Setenv("NODE_DIAGNOSTICS_AI_MODEL", "")
	diagnosis := &NodeDiagnosis{}
	enrichNodeDiagnosis(context.Background(), diagnosis)
	if diagnosis.AIEnabled {
		t.Fatal("AI should remain disabled without provider configuration")
	}
}
