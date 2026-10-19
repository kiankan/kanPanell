package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type nodeAIAnalysis struct {
	Explanation       string   `json:"explanation"`
	Recommendations   []string `json:"recommendations"`
}

type nodeAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// enrichNodeDiagnosis optionally asks an OpenAI-compatible endpoint to explain
// the already-collected evidence. Only sanitized health codes/counts are sent;
// credentials, node addresses, inbound tags and raw error text are never sent.
// AI output is advisory and is never used to execute repairs.
func enrichNodeDiagnosis(parent context.Context, diagnosis *NodeDiagnosis) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("NODE_DIAGNOSTICS_AI_BASE_URL")), "/")
	apiKey := strings.TrimSpace(os.Getenv("NODE_DIAGNOSTICS_AI_API_KEY"))
	model := strings.TrimSpace(os.Getenv("NODE_DIAGNOSTICS_AI_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" || diagnosis == nil {
		return
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return
	}

	type safeFinding struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
	}
	findings := make([]safeFinding, 0, len(diagnosis.Findings))
	for _, finding := range diagnosis.Findings {
		findings = append(findings, safeFinding{Code: finding.Code, Severity: finding.Severity})
	}
	evidence := map[string]any{
		"panel_api_status": diagnosis.Status,
		"xray_state": diagnosis.XrayState,
		"latency_ms": diagnosis.LatencyMs,
		"panel_inbound_count": diagnosis.LocalInboundCount,
		"node_inbound_count": diagnosis.RemoteInboundCount,
		"sync_pending": diagnosis.SyncPending,
		"findings": findings,
	}
	userJSON, err := json.Marshal(evidence)
	if err != nil {
		return
	}
	requestBody, err := json.Marshal(map[string]any{
		"model": model,
		"temperature": 0,
		"max_tokens": 400,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a cautious node-health diagnostician. Explain only what follows from the supplied structured evidence. Return one JSON object with fields explanation (string) and recommendations (array of short strings). Do not invent facts, suggest shell commands, change ports, restart services, or claim an issue is fixed. Recommendations are advisory only. If evidence is insufficient, say so."},
			{"role": "user", "content": string(userJSON)},
		},
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(requestBody))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return
	}
	var chat nodeAIChatResponse
	if err := json.Unmarshal(body, &chat); err != nil || len(chat.Choices) == 0 {
		return
	}
	content := strings.TrimSpace(chat.Choices[0].Message.Content)
	var analysis nodeAIAnalysis
	if err := json.Unmarshal([]byte(content), &analysis); err != nil {
		return
	}
	analysis.Explanation = strings.TrimSpace(analysis.Explanation)
	if len(analysis.Explanation) > 1200 {
		analysis.Explanation = analysis.Explanation[:1200]
	}
	if len(analysis.Recommendations) > 5 {
		analysis.Recommendations = analysis.Recommendations[:5]
	}
	for i := range analysis.Recommendations {
		analysis.Recommendations[i] = strings.TrimSpace(analysis.Recommendations[i])
		if len(analysis.Recommendations[i]) > 300 {
			analysis.Recommendations[i] = analysis.Recommendations[i][:300]
		}
	}
	if analysis.Explanation == "" {
		return
	}
	diagnosis.AIEnabled = true
	diagnosis.AIExplanation = analysis.Explanation
	diagnosis.AIRecommendations = analysis.Recommendations
}

