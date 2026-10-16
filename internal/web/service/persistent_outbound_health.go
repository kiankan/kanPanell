package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/outbound"
)

const (
	outboundHealthConfigKey = "outboundHealthMonitorConfig"
	outboundHealthStateKey  = "outboundHealthMonitorState"
)

var persistentOutboundHealthMu sync.Mutex

type PersistentOutboundHealthConfig struct {
	Enabled          bool   `json:"enabled"`
	IntervalSeconds  int    `json:"intervalSeconds"`
	FailureThreshold int    `json:"failureThreshold"`
	TestMode         string `json:"testMode,omitempty"`
}

type PersistentOutboundHealthStatus struct {
	LastRun     time.Time         `json:"lastRun,omitempty"`
	LastError   string            `json:"lastError,omitempty"`
	Healthy     map[string]bool   `json:"healthy"`
	Consecutive map[string]int    `json:"consecutiveFailures"`
	Managed     map[string]string `json:"managedOverrides"`
}

type outboundHealthState struct {
	Status    PersistentOutboundHealthStatus `json:"status"`
	LastProbe map[string]time.Time           `json:"lastProbe"`
}

func (s *SettingService) GetPersistentOutboundHealthConfig() (PersistentOutboundHealthConfig, error) {
	cfg := PersistentOutboundHealthConfig{
		IntervalSeconds:  60,
		FailureThreshold: 3,
		TestMode:         "tcp",
	}
	raw, err := s.getString(outboundHealthConfigKey)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return cfg, err
		}
	}
	if cfg.IntervalSeconds < 15 {
		cfg.IntervalSeconds = 60
	}
	if cfg.FailureThreshold < 1 || cfg.FailureThreshold > 20 {
		cfg.FailureThreshold = 3
	}
	if cfg.TestMode != "tcp" && cfg.TestMode != "real" {
		cfg.TestMode = "tcp"
	}
	return cfg, nil
}

func (s *SettingService) SavePersistentOutboundHealthConfig(cfg PersistentOutboundHealthConfig) error {
	if cfg.IntervalSeconds < 15 || cfg.IntervalSeconds > 3600 {
		return errors.New("monitor interval must be between 15 and 3600 seconds")
	}
	if cfg.FailureThreshold < 1 || cfg.FailureThreshold > 20 {
		return errors.New("failure threshold must be between 1 and 20")
	}
	if cfg.TestMode == "" {
		cfg.TestMode = "tcp"
	}
	if cfg.TestMode != "tcp" && cfg.TestMode != "real" {
		return errors.New("test mode must be tcp or real")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.saveSetting(outboundHealthConfigKey, string(raw))
}

func (s *SettingService) GetPersistentOutboundHealthStatus() (PersistentOutboundHealthStatus, error) {
	status := PersistentOutboundHealthStatus{
		Healthy:     map[string]bool{},
		Consecutive: map[string]int{},
		Managed:     map[string]string{},
	}
	raw, err := s.getString(outboundHealthStateKey)
	if err != nil {
		return status, err
	}
	state := outboundHealthState{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return status, err
		}
	}
	if state.Status.Healthy != nil {
		status.Healthy = state.Status.Healthy
	}
	if state.Status.Consecutive != nil {
		status.Consecutive = state.Status.Consecutive
	}
	if state.Status.Managed != nil {
		status.Managed = state.Status.Managed
	}
	status.LastRun, status.LastError = state.Status.LastRun, state.Status.LastError
	return status, nil
}

// RunPersistentOutboundHealthMonitor is called by cron and never requires an open browser.
// It is opt-in and only clears overrides that it previously created.
func (s *XrayService) RunPersistentOutboundHealthMonitor() {
	persistentOutboundHealthMu.Lock()
	defer persistentOutboundHealthMu.Unlock()

	settings := &SettingService{}
	cfg, err := settings.GetPersistentOutboundHealthConfig()
	if err != nil || !cfg.Enabled {
		return
	}
	state := outboundHealthState{
		LastProbe: map[string]time.Time{},
		Status: PersistentOutboundHealthStatus{
			Healthy:     map[string]bool{},
			Consecutive: map[string]int{},
			Managed:     map[string]string{},
		},
	}
	if old, e := settings.getString(outboundHealthStateKey); e == nil && old != "" {
		_ = json.Unmarshal([]byte(old), &state)
	}
	if state.LastProbe == nil {
		state.LastProbe = map[string]time.Time{}
	}
	if state.Status.Healthy == nil {
		state.Status.Healthy = map[string]bool{}
	}
	if state.Status.Consecutive == nil {
		state.Status.Consecutive = map[string]int{}
	}
	if state.Status.Managed == nil {
		state.Status.Managed = map[string]string{}
	}

	now := time.Now()
	state.Status.LastError = ""
	defer func() {
		state.Status.LastRun = now
		saved, saveErr := json.Marshal(state)
		if saveErr == nil {
			if saveErr = settings.saveSetting(outboundHealthStateKey, string(saved)); saveErr != nil {
				logger.Warning("outbound health monitor status: ", saveErr)
			}
		} else {
			logger.Warning("marshal outbound health monitor status: ", saveErr)
		}
	}()

	raw, err := settings.GetXrayConfigTemplate()
	if err != nil {
		state.Status.LastError = err.Error()
		logger.Warning("outbound health monitor: ", err)
		return
	}

	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
		Routing   struct {
			Balancers []struct {
				Tag      string   `json:"tag"`
				Selector []string `json:"selector"`
			} `json:"balancers"`
		} `json:"routing"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		state.Status.LastError = err.Error()
		logger.Warning("outbound health monitor config: ", err)
		return
	}

	// Runtime Xray config includes enabled subscription outbounds that are
	// deliberately not written into xrayTemplateConfig. Include them in the
	// same order as GetXrayConfig so balancer selectors and dialerProxy probes
	// see the actual candidate pool.
	prepend, appendList, err := (&OutboundSubscriptionService{}).activeOutboundsSplit()
	if err != nil {
		state.Status.LastError = err.Error()
		logger.Warning("outbound health monitor subscriptions: ", err)
		return
	}
	mergedOutbounds := make([]json.RawMessage, 0, len(prepend)+len(doc.Outbounds)+len(appendList))
	for _, item := range prepend {
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			state.Status.LastError = marshalErr.Error()
			logger.Warning("outbound health monitor subscription outbound: ", marshalErr)
			return
		}
		mergedOutbounds = append(mergedOutbounds, encoded)
	}
	mergedOutbounds = append(mergedOutbounds, doc.Outbounds...)
	for _, item := range appendList {
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			state.Status.LastError = marshalErr.Error()
			logger.Warning("outbound health monitor subscription outbound: ", marshalErr)
			return
		}
		mergedOutbounds = append(mergedOutbounds, encoded)
	}
	doc.Outbounds = mergedOutbounds

	testURL, _ := settings.GetXrayOutboundTestUrl()
	if testURL == "" {
		testURL = "https://www.google.com/generate_204"
	}
	allJSON, _ := json.Marshal(doc.Outbounds)
	probe := outbound.OutboundService{}
	for _, bal := range doc.Routing.Balancers {
		if bal.Tag == "" || len(bal.Selector) == 0 ||
			now.Sub(state.LastProbe[bal.Tag]) < time.Duration(cfg.IntervalSeconds)*time.Second {
			continue
		}

		selectors := make([]*regexp.Regexp, 0, len(bal.Selector))
		selectorInvalid := false
		for _, selector := range bal.Selector {
			compiled, compileErr := regexp.Compile(selector)
			if compileErr != nil {
				state.Status.LastError = "invalid selector for balancer " + bal.Tag + ": " + compileErr.Error()
				selectorInvalid = true
				break
			}
			selectors = append(selectors, compiled)
		}
		if selectorInvalid || len(selectors) == 0 {
			continue
		}

		candidates := []json.RawMessage{}
		candidateTags := map[string]bool{}
		preferred := ""
		for _, rawOB := range doc.Outbounds {
			var ob struct {
				Tag      string `json:"tag"`
				Protocol string `json:"protocol"`
			}
			if json.Unmarshal(rawOB, &ob) != nil || ob.Tag == "" {
				continue
			}
			for _, selector := range selectors {
				if selector.MatchString(ob.Tag) &&
					ob.Protocol != "freedom" && ob.Protocol != "blackhole" && ob.Protocol != "dns" {
					candidates = append(candidates, rawOB)
					candidateTags[ob.Tag] = true
					if preferred == "" {
						preferred = ob.Tag
					}
					break
				}
			}
		}
		if len(candidates) == 0 {
			continue
		}

		state.LastProbe[bal.Tag] = now
		batch, _ := json.Marshal(candidates)
		results, e := probe.TestOutbounds(string(batch), testURL, string(allJSON), cfg.TestMode)
		if e != nil {
			state.Status.LastError = e.Error()
			continue
		}
		healthy := map[string]bool{}
		for _, result := range results {
			if result != nil {
				healthy[result.Tag] = result.Success
				state.Status.Healthy[result.Tag] = result.Success
			}
		}

		statuses, e := s.GetBalancersStatus([]string{bal.Tag})
		if e != nil {
			state.Status.LastError = e.Error()
			continue
		}
		if len(statuses) == 0 || !statuses[0].Running {
			continue
		}
		live := statuses[0]
		// Xray's strategy target list is the live source of truth. The first
		// configured outbound is only a fallback for older/empty live status.
		for _, selected := range live.Selected {
			if candidateTags[selected] {
				preferred = selected
				break
			}
		}
		managed := state.Status.Managed[bal.Tag]
		if live.Override != "" && managed == "" {
			continue
		}
		if live.Override != "" && managed != "" && live.Override != managed {
			delete(state.Status.Managed, bal.Tag)
			continue
		}

		current := preferred
		if live.Override != "" {
			current = live.Override
		}
		if healthy[current] {
			state.Status.Consecutive[bal.Tag] = 0
			if managed != "" && live.Override == managed && healthy[preferred] {
				if e := s.OverrideBalancer(bal.Tag, ""); e == nil {
					delete(state.Status.Managed, bal.Tag)
				} else {
					state.Status.LastError = e.Error()
				}
			}
			continue
		}

		state.Status.Consecutive[bal.Tag]++
		if state.Status.Consecutive[bal.Tag] < cfg.FailureThreshold {
			continue
		}
		target := ""
		for _, rawOB := range candidates {
			var ob struct {
				Tag string `json:"tag"`
			}
			_ = json.Unmarshal(rawOB, &ob)
			if ob.Tag != current && healthy[ob.Tag] {
				target = ob.Tag
				break
			}
		}
		// Re-failover only when the existing override belongs to this monitor.
		// Manual overrides are left untouched, but a monitor-managed override
		// can be replaced if its target has also become unhealthy.
		canChangeOverride := live.Override == "" || (managed != "" && live.Override == managed)
		if target != "" && canChangeOverride {
			if e := s.OverrideBalancer(bal.Tag, target); e != nil {
				state.Status.LastError = e.Error()
			} else {
				state.Status.Managed[bal.Tag] = target
				state.Status.Consecutive[bal.Tag] = 0
			}
		}
	}
}
