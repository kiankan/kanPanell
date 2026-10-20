package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// NodeDiagnosticFinding is an actionable, explainable health finding. The
// analyzer intentionally uses observed panel/API data only; it never guesses
// at remote shell state or invents configuration values.
type NodeDiagnosticFinding struct {
	Code           string `json:"code"`
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Detail         string `json:"detail"`
	Recommendation string `json:"recommendation"`
}

// NodeRepairAction is a narrowly-scoped action that requires an explicit
// operator confirmation before execution.
type NodeRepairAction struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// NodeDiagnosticReport summarizes a live check against the configured node.
type NodeDiagnosticReport struct {
	NodeID        int                     `json:"nodeId"`
	NodeName      string                  `json:"nodeName"`
	CheckedAt     time.Time               `json:"checkedAt"`
	Status        string                  `json:"status"`
	XrayState     string                  `json:"xrayState,omitempty"`
	XrayError     string                  `json:"xrayError,omitempty"`
	InboundCount  int                     `json:"inboundCount"`
	InboundStatus string                  `json:"inboundStatus"`
	Findings      []NodeDiagnosticFinding `json:"findings"`
	RepairActions []NodeRepairAction      `json:"repairActions"`
	// AI fields are populated only when an OpenAI-compatible provider is
	// configured (see node_diagnostics_ai.go); they are advisory text and
	// never drive a repair.
	AIEnabled         bool     `json:"aiEnabled"`
	AIExplanation     string   `json:"aiExplanation,omitempty"`
	AIRecommendations []string `json:"aiRecommendations,omitempty"`
}

// BuildNodeDiagnosticReport applies deterministic rules to the latest live
// probe and inbound API results. It does not claim to use a generative AI
// provider: this remains safe and useful when no AI provider is configured.
func BuildNodeDiagnosticReport(node *model.Node, patch HeartbeatPatch, probeErr error, inboundCount int, inboundErr error) NodeDiagnosticReport {
	report := NodeDiagnosticReport{
		CheckedAt:     time.Now().UTC(),
		Status:        patch.Status,
		XrayState:     strings.ToLower(strings.TrimSpace(patch.XrayState)),
		XrayError:     strings.TrimSpace(patch.XrayError),
		InboundCount:  inboundCount,
		InboundStatus: "not_checked",
		Findings:      make([]NodeDiagnosticFinding, 0, 3),
		RepairActions: make([]NodeRepairAction, 0, 1),
	}
	if node != nil {
		report.NodeID = node.Id
		report.NodeName = node.Name
	}
	if probeErr == nil && inboundErr != nil {
		report.InboundStatus = "unavailable"
	} else if probeErr == nil {
		report.InboundStatus = "ok"
	}
	if probeErr != nil || report.Status != "online" {
		detail := "The node panel API could not be reached."
		if probeErr != nil {
			detail = fmt.Sprintf("The node panel API probe failed: %s", probeErr.Error())
		} else if patch.LastError != "" {
			detail = patch.LastError
		}
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "panel_unreachable",
			Severity:       "error",
			Title:          "Node panel API is unreachable",
			Detail:         detail,
			Recommendation: "Verify the scheme, address, API port, base path, API token or mTLS setup, TLS verification mode, and network/firewall access. Re-run diagnosis after correcting access.",
		})
		return report
	}

	switch report.XrayState {
	case "error":
		detail := report.XrayError
		if detail == "" {
			detail = "The node panel is reachable, but its status endpoint reports an Xray error."
		}
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "xray_error",
			Severity:       "error",
			Title:          "Xray reports an error",
			Detail:         detail,
			Recommendation: "Review the Xray error details. A confirmed restart is available below; restart may briefly interrupt active connections.",
		})
		report.RepairActions = append(report.RepairActions, NodeRepairAction{
			ID:          "restart_xray",
			Label:       "Restart Xray on this node",
			Description: "Restart the remote Xray service through the authenticated node panel API, then probe the node again. No inbound configuration will be changed.",
		})
	case "stop", "stopped":
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "xray_stopped",
			Severity:       "error",
			Title:          "Xray is stopped",
			Detail:         "The node panel API is reachable, but Xray reports a stopped state.",
			Recommendation: "A confirmed restart is available below. If Xray fails again, inspect the node's Xray logs and configuration validation.",
		})
		report.RepairActions = append(report.RepairActions, NodeRepairAction{
			ID:          "restart_xray",
			Label:       "Start Xray again",
			Description: "Request a restart through the authenticated node panel API and verify the result. No inbound configuration will be changed.",
		})
	case "", "unknown":
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "xray_state_unknown",
			Severity:       "warning",
			Title:          "Xray state is unknown",
			Detail:         "The node panel API responded, but did not provide a recognized Xray state.",
			Recommendation: "Check the remote panel version and Xray status page. No automatic repair is offered because the service state could not be verified.",
		})
	default:
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "xray_healthy",
			Severity:       "info",
			Title:          "Xray is responding",
			Detail:         "The node panel API reports Xray state: " + report.XrayState + ".",
			Recommendation: "If clients still cannot connect, check the inbound's listen address and port, firewall rules, DNS, transport/security settings, and client configuration.",
		})
	}

	if inboundErr != nil {
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "inbounds_unavailable",
			Severity:       "warning",
			Title:          "Could not inspect remote inbounds",
			Detail:         "The node status was reachable, but the inbound list API failed: " + inboundErr.Error(),
			Recommendation: "Check the node API permissions and panel compatibility, then retry. No inbound changes were attempted.",
		})
	} else if inboundCount == 0 {
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "no_inbounds",
			Severity:       "warning",
			Title:          "No inbounds found on the node",
			Detail:         "The remote panel returned an empty inbound list.",
			Recommendation: "Create an inbound using the normal inbound editor and select this node. A protocol, port, and TLS/Reality settings cannot be safely inferred from health data alone.",
		})
	}

	if len(report.Findings) == 0 {
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "healthy",
			Severity:       "info",
			Title:          "Node checks passed",
			Detail:         "The panel API and inbound list are reachable.",
			Recommendation: "If users still report connection failures, test a client connection and verify the public listener port and firewall.",
		})
	}
	return report
}

// RestartRemoteXray calls only the existing authenticated node-panel restart
// endpoint. It never runs commands on the host or changes inbound configuration.
func (s *NodeService) RestartRemoteXray(ctx context.Context, id int) error {
	if id <= 0 {
		return errors.New("invalid node id")
	}
	n, err := s.GetById(id)
	if err != nil {
		return err
	}
	if n.OutboundTag == "" {
		return runtime.NewRemote(n, nil).RestartXray(ctx)
	}
	var restartErr error
	s.withOutboundBridge(n.Id, n.OutboundTag, func(proxyURL string) {
		restartErr = runtime.NewRemote(n, staticEgressResolver(proxyURL)).RestartXray(ctx)
	})
	return restartErr
}
