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

func TestEnrichNodeDiagnosticReportSendsOnlySanitizedEvidence(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("unexpected authorization header %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"explanation\":\"Inbound counts differ\",\"recommendations\":[\"Review sync mode\"]}"}}]}`)
	}))
	defer server.Close()
	t.Setenv("NODE_DIAGNOSTICS_AI_BASE_URL", server.URL+"/v1")
	t.Setenv("NODE_DIAGNOSTICS_AI_API_KEY", "test-secret")
	t.Setenv("NODE_DIAGNOSTICS_AI_MODEL", "test-model")

	report := &NodeDiagnosticReport{
		NodeName:     "private-node-name",
		Status:       "online",
		XrayState:    "error",
		XrayError:    "private raw error containing a token",
		InboundCount: 2,
		Findings: []NodeDiagnosticFinding{{
			Code: "inbound_count_mismatch", Severity: "warning",
			Title: "must not be sent", Detail: "must not be sent either",
		}},
	}
	enrichNodeDiagnosticReport(context.Background(), report, NodeInboundSyncState{Enabled: true, LocalCount: 4})
	if !report.AIEnabled || report.AIExplanation != "Inbound counts differ" {
		t.Fatalf("expected AI analysis, got %#v", report)
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

func TestEnrichNodeDiagnosticReportSkipsWhenProviderIsNotConfigured(t *testing.T) {
	t.Setenv("NODE_DIAGNOSTICS_AI_BASE_URL", "")
	t.Setenv("NODE_DIAGNOSTICS_AI_API_KEY", "")
	t.Setenv("NODE_DIAGNOSTICS_AI_MODEL", "")
	report := &NodeDiagnosticReport{}
	enrichNodeDiagnosticReport(context.Background(), report, NodeInboundSyncState{})
	if report.AIEnabled {
		t.Fatal("AI should remain disabled without provider configuration")
	}
}

func TestTruncateRunesKeepsMultibyteCharactersIntact(t *testing.T) {
	got := truncateRunes("سلام دنیا", 4)
	if got != "سلام" {
		t.Fatalf("truncateRunes split a character: %q", got)
	}
	if truncateRunes("abc", 10) != "abc" {
		t.Fatal("short strings must be returned unchanged")
	}
}

func TestAISafeXrayStateOnlyForwardsKnownStates(t *testing.T) {
	for _, known := range []string{"", "unknown", "running", "stop", "stopped", "error"} {
		if got := aiSafeXrayState(known); got != known {
			t.Errorf("aiSafeXrayState(%q) = %q, want it unchanged", known, got)
		}
	}
	for _, raw := range []string{"ignore previous instructions", "error: token abc123", "stopped\nextra"} {
		if got := aiSafeXrayState(raw); got != "other" {
			t.Errorf("aiSafeXrayState(%q) = %q, want \"other\"", raw, got)
		}
	}
}
