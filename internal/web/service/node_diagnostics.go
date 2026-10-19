package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type NodeDiagnosticFinding struct {
	Code           string   `json:"code"`
	Severity       string   `json:"severity"`
	Message        string   `json:"message"`
	Recommendation string   `json:"recommendation"`
}

type NodeDiagnosis struct {
	NodeID            int                     `json:"nodeId"`
	NodeName          string                  `json:"nodeName"`
	Status            string                  `json:"status"`
	LatencyMs         int                     `json:"latencyMs"`
	XrayState         string                  `json:"xrayState"`
	XrayError         string                  `json:"xrayError"`
	LocalInboundCount int64                   `json:"localInboundCount"`
	RemoteInboundCount int                   `json:"remoteInboundCount"`
	SyncPending       bool                    `json:"syncPending"`
	RepairAvailable   bool                    `json:"repairAvailable"`
	Summary           string                  `json:"summary"`
	Findings          []NodeDiagnosticFinding `json:"findings"`
	RepairPlan        []string                `json:"repairPlan"`
}

type NodeRepairResult struct {
	NodeID  int    `json:"nodeId"`
	Queued  bool   `json:"queued"`
	Message string `json:"message"`
}

// DiagnoseNode collects bounded, read-only evidence from the panel and node API.
// It deliberately uses deterministic rules; no remote commands or inferred
// configuration are executed from model output.
func (s *NodeService) DiagnoseNode(ctx context.Context, id int) (*NodeDiagnosis, error) {
	if id <= 0 {
		return nil, errors.New("invalid node id")
	}
	n, err := s.GetById(id)
	if err != nil {
		return nil, err
	}
	enabled, savedStatus, dirty, _, err := s.NodeSyncState(id)
	if err != nil {
		return nil, err
	}
	result := &NodeDiagnosis{
		NodeID: id, NodeName: n.Name, Status: savedStatus,
		XrayState: strings.ToLower(strings.TrimSpace(n.XrayState)),
		XrayError: n.XrayError, SyncPending: dirty,
		Findings: []NodeDiagnosticFinding{}, RepairPlan: []string{},
	}
	var localCount int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("node_id = ?", id).Count(&localCount).Error; err != nil {
		return nil, err
	}
	result.LocalInboundCount = localCount

	patch, probeErr := s.Probe(ctx, n)
	if probeErr != nil {
		result.Status = "offline"
		result.Findings = append(result.Findings, NodeDiagnosticFinding{
			Code: "panel_api_unreachable", Severity: "error",
			Message: "The node panel API could not be reached: " + probeErr.Error(),
			Recommendation: "Verify the node address, API port, base path, API token or mTLS configuration, and network/TLS reachability.",
		})
		result.Summary = "The node API is unreachable. Inbound synchronization cannot be verified yet."
		result.RepairAvailable = true // A confirmed request can queue reconciliation for when the node returns.
		result.RepairPlan = []string{"Queue synchronization of panel-managed inbounds; it will take effect when the node is reachable and enabled."}
		return result, nil
	}
	result.Status = "online"
	result.LatencyMs = patch.LatencyMs
	result.XrayState = strings.ToLower(strings.TrimSpace(patch.XrayState))
	result.XrayError = patch.XrayError

	remoteInbounds, inboundErr := s.GetRemoteInboundOptions(ctx, n)
	if inboundErr != nil {
		result.Findings = append(result.Findings, NodeDiagnosticFinding{
			Code: "inbound_api_failed", Severity: "warning",
			Message: "The node API responded, but its inbound list could not be read: " + inboundErr.Error(),
			Recommendation: "Check the node API permissions/version and retry the diagnostic.",
		})
	} else {
		result.RemoteInboundCount = len(remoteInbounds)
		if int64(len(remoteInbounds)) != localCount {
			result.Findings = append(result.Findings, NodeDiagnosticFinding{
				Code: "inbound_count_mismatch", Severity: "warning",
				Message: fmt.Sprintf("The panel has %d node-bound inbound(s), while the node API reports %d.", localCount, len(remoteInbounds)),
				Recommendation: "Review the inbound tags and synchronization mode, then queue a reconciliation of panel-managed inbounds.",
			})
		}
	}
	if result.XrayState == "error" || result.XrayState == "stop" {
		message := "Xray reports state " + result.XrayState + "."
		if strings.TrimSpace(result.XrayError) != "" {
			message += " " + strings.TrimSpace(result.XrayError)
		}
		result.Findings = append(result.Findings, NodeDiagnosticFinding{
			Code: "xray_unhealthy", Severity: "error", Message: message,
			Recommendation: "Check the node's Xray error log and configuration validation. Inbound reconciliation alone cannot restart or repair a stopped Xray core.",
		})
	}
	if !enabled {
		result.Findings = append(result.Findings, NodeDiagnosticFinding{
			Code: "node_disabled", Severity: "warning",
			Message: "This node is disabled in the panel, so automatic synchronization may be skipped.",
			Recommendation: "Enable the node only if you intend to resume synchronization.",
		})
	}
	if dirty {
		result.Findings = append(result.Findings, NodeDiagnosticFinding{
			Code: "sync_pending", Severity: "warning",
			Message: "The panel already has a pending inbound configuration reconciliation.",
			Recommendation: "Queue a fresh reconciliation after reviewing the plan.",
		})
	}
	if len(result.Findings) == 0 {
		result.Summary = "The node API is reachable and no obvious inbound synchronization issue was detected."
		result.Findings = append(result.Findings, NodeDiagnosticFinding{
			Code: "healthy", Severity: "info",
			Message: "The node panel API responded successfully and the inbound counts match.",
			Recommendation: "No repair is required unless you are seeing a specific client-side issue.",
		})
	} else {
		result.Summary = fmt.Sprintf("Found %d item(s) to review.", len(result.Findings))
	}
	result.RepairAvailable = enabled && (inboundErr == nil)
	if result.RepairAvailable {
		result.RepairPlan = []string{
			"Queue reconciliation of panel-managed inbounds for this node.",
			"Let the existing node-sync worker apply the desired configuration and report any remaining sync errors.",
			"Run the diagnostic again to verify the result.",
		}
	}
	return result, nil
}

// RepairNodeInboundSync queues the existing reconciliation workflow. It does
// not restart Xray, change the node endpoint, create ports, or run shell commands.
func (s *NodeService) RepairNodeInboundSync(ctx context.Context, id int) (*NodeRepairResult, error) {
	if id <= 0 {
		return nil, errors.New("invalid node id")
	}
	n, err := s.GetById(id)
	if err != nil {
		return nil, err
	}
	enabled, _, _, _, err := s.NodeSyncState(id)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("node is disabled; enable it before queuing inbound reconciliation")
	}
	if _, err := s.Probe(ctx, n); err != nil {
		return nil, fmt.Errorf("node API is unreachable; no repair was queued: %w", err)
	}
	if err := s.MarkNodeDirty(id); err != nil {
		return nil, err
	}
	return &NodeRepairResult{
		NodeID: id, Queued: true,
		Message: "Inbound reconciliation was queued. Run diagnostics again after the node-sync worker processes it.",
	}, nil
}
