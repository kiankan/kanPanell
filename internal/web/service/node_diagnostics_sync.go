package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// RepairActionInboundSync is the allowlisted action id that queues the
// existing panel-managed inbound reconciliation for one node.
const RepairActionInboundSync = "inbound_sync"

// NodeInboundSyncState is the panel-side evidence used to decide whether a
// reconciliation is worth offering: whether the node is enabled, whether a
// reconciliation is already pending, and how many inbounds the panel has
// bound to the node.
type NodeInboundSyncState struct {
	Enabled    bool
	Pending    bool
	LocalCount int64
}

// InboundSyncState reads the panel's own view of a node's inbound sync.
func (s *NodeService) InboundSyncState(id int) (NodeInboundSyncState, error) {
	enabled, _, dirty, _, err := s.NodeSyncState(id)
	if err != nil {
		return NodeInboundSyncState{}, err
	}
	var local int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("node_id = ?", id).Count(&local).Error; err != nil {
		return NodeInboundSyncState{}, err
	}
	return NodeInboundSyncState{Enabled: enabled, Pending: dirty, LocalCount: local}, nil
}

// ApplyInboundSyncFindings adds inbound-synchronization findings to a report
// built from a live probe and, only when there is a concrete reason (the
// node's inbound count differs from the panel's, or a reconciliation is
// already pending), offers the explicit inbound_sync repair action. It never
// invents a repair when nothing indicates drift.
func ApplyInboundSyncFindings(report *NodeDiagnosticReport, state NodeInboundSyncState) {
	if report == nil || report.InboundStatus != "ok" {
		return
	}
	mismatch := int64(report.InboundCount) != state.LocalCount
	if mismatch {
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:     "inbound_count_mismatch",
			Severity: "warning",
			Title:    "Inbound counts differ",
			Detail: fmt.Sprintf("The panel has %d node-bound inbound(s), while the node API reports %d.",
				state.LocalCount, report.InboundCount),
			Recommendation: "Review the inbound tags and synchronization mode, then queue a reconciliation of panel-managed inbounds.",
		})
	}
	if state.Pending {
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "sync_pending",
			Severity:       "warning",
			Title:          "Reconciliation already pending",
			Detail:         "The panel already has a pending inbound configuration reconciliation for this node.",
			Recommendation: "Wait for the node-sync worker, then run diagnostics again.",
		})
	}
	if !state.Enabled {
		report.Findings = append(report.Findings, NodeDiagnosticFinding{
			Code:           "node_disabled",
			Severity:       "warning",
			Title:          "Node is disabled",
			Detail:         "This node is disabled in the panel, so automatic synchronization may be skipped.",
			Recommendation: "Enable the node only if you intend to resume synchronization.",
		})
		return
	}
	if mismatch || state.Pending {
		report.RepairActions = append(report.RepairActions, NodeRepairAction{
			ID:    RepairActionInboundSync,
			Label: "Queue inbound reconciliation",
			Description: "Queues the panel's existing reconciliation of panel-managed inbounds for this node. " +
				"It does not restart Xray, change the node endpoint, create ports, or run shell commands.",
		})
	}
}

// QueueInboundSync queues the existing reconciliation workflow for a node
// after re-checking that the node is enabled and its panel API is reachable.
// It returns a human-readable message; verification happens on the next
// diagnostic run once the node-sync worker has processed the queue.
func (s *NodeService) QueueInboundSync(ctx context.Context, id int) (string, error) {
	if id <= 0 {
		return "", errors.New("invalid node id")
	}
	n, err := s.GetById(id)
	if err != nil {
		return "", err
	}
	enabled, _, _, _, err := s.NodeSyncState(id)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "", errors.New("node is disabled; enable it before queuing inbound reconciliation")
	}
	if _, err := s.Probe(ctx, n); err != nil {
		return "", fmt.Errorf("node API is unreachable; no repair was queued: %w", err)
	}
	if err := s.MarkNodeDirty(id); err != nil {
		return "", err
	}
	return "Inbound reconciliation was queued. Run diagnostics again after the node-sync worker processes it.", nil
}

// CompleteNodeDiagnosticReport layers the panel-side inbound-sync evidence and
// the optional AI explanation onto a live report. Failures here degrade the
// report gracefully instead of failing the whole diagnostic.
func (s *NodeService) CompleteNodeDiagnosticReport(ctx context.Context, id int, report *NodeDiagnosticReport) {
	if report == nil {
		return
	}
	state, err := s.InboundSyncState(id)
	if err == nil {
		ApplyInboundSyncFindings(report, state)
	}
	enrichNodeDiagnosticReport(ctx, report, state)
}
