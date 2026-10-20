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
