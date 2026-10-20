package service

import (
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestBuildNodeDiagnosticReportUnreachableHasNoRepair(t *testing.T) {
	node := &model.Node{Id: 7, Name: "edge-1"}
	report := BuildNodeDiagnosticReport(node, HeartbeatPatch{Status: "offline", LastError: "connection refused"}, errors.New("connection refused"), 0, nil)
	if report.NodeID != 7 || report.Status != "offline" {
		t.Fatalf("unexpected node report: %+v", report)
	}
	if len(report.RepairActions) != 0 {
		t.Fatalf("unreachable node must not expose repair actions: %+v", report.RepairActions)
	}
	if len(report.Findings) == 0 || report.Findings[0].Code != "panel_unreachable" {
		t.Fatalf("expected panel_unreachable finding, got %+v", report.Findings)
	}
}

func TestBuildNodeDiagnosticReportXrayStoppedOffersExplicitRestart(t *testing.T) {
	node := &model.Node{Id: 3, Name: "edge-2"}
	report := BuildNodeDiagnosticReport(node, HeartbeatPatch{Status: "online", XrayState: "stop"}, nil, 2, nil)
	if len(report.RepairActions) != 1 || report.RepairActions[0].ID != "restart_xray" {
		t.Fatalf("expected restart action, got %+v", report.RepairActions)
	}
	if len(report.Findings) == 0 || report.Findings[0].Code != "xray_stopped" {
		t.Fatalf("expected xray_stopped finding, got %+v", report.Findings)
	}
}

func TestBuildNodeDiagnosticReportNoInboundDoesNotInventRepair(t *testing.T) {
	node := &model.Node{Id: 4, Name: "edge-3"}
	report := BuildNodeDiagnosticReport(node, HeartbeatPatch{Status: "online", XrayState: "running"}, nil, 0, nil)
	if len(report.RepairActions) != 0 {
		t.Fatalf("must not invent an inbound repair without protocol details: %+v", report.RepairActions)
	}
	found := false
	for _, f := range report.Findings {
		if f.Code == "no_inbounds" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected no_inbounds finding, got %+v", report.Findings)
	}
}

func okReport(remote int) NodeDiagnosticReport {
	return BuildNodeDiagnosticReport(&model.Node{Id: 9, Name: "edge-9"}, HeartbeatPatch{Status: "online", XrayState: "running"}, nil, remote, nil)
}

func hasFinding(r NodeDiagnosticReport, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestApplyInboundSyncFindingsOffersRepairOnlyOnDrift(t *testing.T) {
	report := okReport(2)
	ApplyInboundSyncFindings(&report, NodeInboundSyncState{Enabled: true, LocalCount: 2})
	if len(report.RepairActions) != 0 || hasFinding(report, "inbound_count_mismatch") {
		t.Fatalf("matching counts must not offer a repair: %+v", report)
	}

	drift := okReport(1)
	ApplyInboundSyncFindings(&drift, NodeInboundSyncState{Enabled: true, LocalCount: 3})
	if !hasFinding(drift, "inbound_count_mismatch") {
		t.Fatalf("expected inbound_count_mismatch, got %+v", drift.Findings)
	}
	found := false
	for _, a := range drift.RepairActions {
		if a.ID == RepairActionInboundSync {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected inbound_sync action on drift, got %+v", drift.RepairActions)
	}
}

func TestApplyInboundSyncFindingsNeverRepairsDisabledOrUnreachableNodes(t *testing.T) {
	disabled := okReport(1)
	ApplyInboundSyncFindings(&disabled, NodeInboundSyncState{Enabled: false, LocalCount: 3, Pending: true})
	if len(disabled.RepairActions) != 0 || !hasFinding(disabled, "node_disabled") {
		t.Fatalf("disabled node must report node_disabled and offer no repair: %+v", disabled)
	}

	unreachable := BuildNodeDiagnosticReport(&model.Node{Id: 1}, HeartbeatPatch{Status: "offline"}, errors.New("refused"), 0, nil)
	before := len(unreachable.Findings)
	ApplyInboundSyncFindings(&unreachable, NodeInboundSyncState{Enabled: true, LocalCount: 5, Pending: true})
	if len(unreachable.RepairActions) != 0 || len(unreachable.Findings) != before {
		t.Fatalf("unreachable node must be left untouched: %+v", unreachable)
	}
}
