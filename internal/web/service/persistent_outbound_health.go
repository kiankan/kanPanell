package service

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/outbound"
)

const outboundHealthConfigKey = "outboundHealthMonitorConfig"
const outboundHealthStateKey = "outboundHealthMonitorState"

var persistentOutboundHealthMu sync.Mutex

type PersistentOutboundHealthConfig struct {
	Enabled bool `json:"enabled"`
	IntervalSeconds int `json:"intervalSeconds"`
	FailureThreshold int `json:"failureThreshold"`
	TestMode string `json:"testMode,omitempty"`
}
type PersistentOutboundHealthStatus struct {
	LastRun time.Time `json:"lastRun,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Healthy map[string]bool `json:"healthy"`
	Consecutive map[string]int `json:"consecutiveFailures"`
	Managed map[string]string `json:"managedOverrides"`
}
type outboundHealthState struct {
	Status PersistentOutboundHealthStatus `json:"status"`
	LastProbe map[string]time.Time `json:"lastProbe"`
}

func (s *SettingService) GetPersistentOutboundHealthConfig() (PersistentOutboundHealthConfig, error) {
	cfg := PersistentOutboundHealthConfig{IntervalSeconds:60, FailureThreshold:3, TestMode:"tcp"}
	raw, err := s.getString(outboundHealthConfigKey); if err != nil { return cfg, err }
	if strings.TrimSpace(raw) != "" { if err := json.Unmarshal([]byte(raw), &cfg); err != nil { return cfg, err } }
	if cfg.IntervalSeconds < 15 { cfg.IntervalSeconds = 60 }; if cfg.FailureThreshold < 1 || cfg.FailureThreshold > 20 { cfg.FailureThreshold = 3 }
	if cfg.TestMode != "tcp" && cfg.TestMode != "real" { cfg.TestMode = "tcp" }
	return cfg, nil
}
func (s *SettingService) SavePersistentOutboundHealthConfig(cfg PersistentOutboundHealthConfig) error {
	if cfg.IntervalSeconds < 15 || cfg.IntervalSeconds > 3600 { return errors.New("monitor interval must be between 15 and 3600 seconds") }
	if cfg.FailureThreshold < 1 || cfg.FailureThreshold > 20 { return errors.New("failure threshold must be between 1 and 20") }
	if cfg.TestMode == "" { cfg.TestMode = "tcp" }; if cfg.TestMode != "tcp" && cfg.TestMode != "real" { return errors.New("test mode must be tcp or real") }
	raw, err := json.Marshal(cfg); if err != nil { return err }; return s.saveSetting(outboundHealthConfigKey, string(raw))
}
func (s *SettingService) GetPersistentOutboundHealthStatus() (PersistentOutboundHealthStatus, error) {
	status := PersistentOutboundHealthStatus{Healthy:map[string]bool{}, Consecutive:map[string]int{}, Managed:map[string]string{}}
	raw, err := s.getString(outboundHealthStateKey); if err != nil { return status, err }
	state := outboundHealthState{}; if raw != "" { if err := json.Unmarshal([]byte(raw), &state); err != nil { return status, err } }
	if state.Status.Healthy != nil { status.Healthy = state.Status.Healthy }; if state.Status.Consecutive != nil { status.Consecutive = state.Status.Consecutive }; if state.Status.Managed != nil { status.Managed = state.Status.Managed }
	status.LastRun, status.LastError = state.Status.LastRun, state.Status.LastError
	return status, nil
}

// RunPersistentOutboundHealthMonitor is called by cron and never requires an open browser.
// It is opt-in and only clears overrides that it previously created.
func (s *XrayService) RunPersistentOutboundHealthMonitor() {
	persistentOutboundHealthMu.Lock()
	defer persistentOutboundHealthMu.Unlock()
	settings := &SettingService{}; cfg, err := settings.GetPersistentOutboundHealthConfig(); if err != nil || !cfg.Enabled { return }
	raw, err := settings.GetXrayConfigTemplate(); if err != nil { logger.Warning("outbound health monitor: ", err); return }
	var doc struct { Outbounds []json.RawMessage `json:"outbounds"`; Routing struct { Balancers []struct { Tag string `json:"tag"`; Selector []string `json:"selector"` } `json:"balancers"` } `json:"routing"` }
	if err := json.Unmarshal([]byte(raw), &doc); err != nil { logger.Warning("outbound health monitor config: ", err); return }
	state := outboundHealthState{LastProbe:map[string]time.Time{}, Status:PersistentOutboundHealthStatus{Healthy:map[string]bool{},Consecutive:map[string]int{},Managed:map[string]string{}}}
	if old, e := settings.getString(outboundHealthStateKey); e == nil && old != "" { _ = json.Unmarshal([]byte(old), &state) }
	if state.LastProbe == nil { state.LastProbe = map[string]time.Time{} }; if state.Status.Healthy == nil { state.Status.Healthy = map[string]bool{} }; if state.Status.Consecutive == nil { state.Status.Consecutive = map[string]int{} }; if state.Status.Managed == nil { state.Status.Managed = map[string]string{} }
	now := time.Now(); testURL, _ := settings.GetXrayOutboundTestUrl(); if testURL == "" { testURL = "https://www.google.com/generate_204" }; allJSON, _ := json.Marshal(doc.Outbounds); probe := outbound.OutboundService{}
	for _, bal := range doc.Routing.Balancers {
		if bal.Tag == "" || len(bal.Selector) == 0 || now.Sub(state.LastProbe[bal.Tag]) < time.Duration(cfg.IntervalSeconds)*time.Second { continue }
		candidates := []json.RawMessage{}; preferred := ""
		for _, rawOB := range doc.Outbounds { var ob struct { Tag string `json:"tag"`; Protocol string `json:"protocol"` }; if json.Unmarshal(rawOB,&ob)!=nil || ob.Tag=="" { continue }; for _, prefix := range bal.Selector { if strings.HasPrefix(ob.Tag,prefix) && ob.Protocol!="freedom" && ob.Protocol!="blackhole" && ob.Protocol!="dns" { candidates=append(candidates,rawOB); if preferred=="" { preferred=ob.Tag }; break } } }
		if len(candidates)==0 { continue }; state.LastProbe[bal.Tag]=now; batch,_:=json.Marshal(candidates); results,e:=probe.TestOutbounds(string(batch),testURL,string(allJSON),cfg.TestMode); if e!=nil { state.Status.LastError=e.Error(); continue }
		healthy:=map[string]bool{}; for _, result:=range results { if result!=nil { healthy[result.Tag]=result.Success; state.Status.Healthy[result.Tag]=result.Success } }
		statuses,e:=s.GetBalancersStatus([]string{bal.Tag}); if e!=nil || len(statuses)==0 || !statuses[0].Running { continue }; live:=statuses[0]; managed:=state.Status.Managed[bal.Tag]
		if live.Override!="" && managed=="" { continue }; if live.Override!="" && managed!="" && live.Override!=managed { delete(state.Status.Managed,bal.Tag); continue }
		current:=preferred; if live.Override!="" { current=live.Override }; if healthy[current] { state.Status.Consecutive[bal.Tag]=0; if managed!="" && live.Override==managed && healthy[preferred] { if e:=s.OverrideBalancer(bal.Tag,""); e==nil { delete(state.Status.Managed,bal.Tag) } else { state.Status.LastError=e.Error() } }; continue }
		state.Status.Consecutive[bal.Tag]++; if state.Status.Consecutive[bal.Tag]<cfg.FailureThreshold { continue }; target:=""; for _, rawOB:=range candidates { var ob struct { Tag string `json:"tag"` }; _=json.Unmarshal(rawOB,&ob); if ob.Tag!=current && healthy[ob.Tag] { target=ob.Tag; break } }; if target!="" && live.Override=="" { if e:=s.OverrideBalancer(bal.Tag,target); e!=nil { state.Status.LastError=e.Error() } else { state.Status.Managed[bal.Tag]=target; state.Status.Consecutive[bal.Tag]=0 } }
	}
	state.Status.LastRun=now; saved,e:=json.Marshal(state); if e==nil { if e=settings.saveSetting(outboundHealthStateKey,string(saved)); e!=nil { logger.Warning("outbound health monitor status: ",e) } }
}
