package service

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const (
	inboundRouteRulePrefix     = "inbound-route:"
	inboundRouteBalancerPrefix = "inbound-route-bal:"
)

func inboundRouteRuleTag(inboundTag string) string {
	return inboundRouteRulePrefix + inboundTag
}

func inboundRouteBalancerTag(inboundTag string) string {
	return inboundRouteBalancerPrefix + inboundTag
}

// GetInboundRouting returns the outbound tags that the panel-managed routing
// rule for inboundTag currently sends traffic to. Empty when no managed rule
// exists (traffic then follows the normal routing rules / default outbound).
func (s *XraySettingService) GetInboundRouting(inboundTag string) ([]string, error) {
	if inboundTag == "" {
		return []string{}, nil
	}
	templateStr, err := s.GetXrayConfigTemplate()
	if err != nil {
		return nil, common.NewError("failed to read xray template:", err)
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(templateStr), &template); err != nil {
		return nil, common.NewError("xray template is not valid JSON:", err)
	}
	routing, _ := template["routing"].(map[string]any)
	if routing == nil {
		return []string{}, nil
	}
	ruleTag := inboundRouteRuleTag(inboundTag)
	rules, _ := routing["rules"].([]any)
	for _, r := range rules {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := rm["ruleTag"].(string); t != ruleTag {
			continue
		}
		if ot, _ := rm["outboundTag"].(string); ot != "" {
			return []string{ot}, nil
		}
		if bt, _ := rm["balancerTag"].(string); bt != "" {
			if bt != inboundRouteBalancerTag(inboundTag) {
				return []string{bt}, nil
			}
			balancers, _ := routing["balancers"].([]any)
			for _, b := range balancers {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := bm["tag"].(string); t != bt {
					continue
				}
				sel, _ := bm["selector"].([]any)
				out := make([]string, 0, len(sel))
				for _, v := range sel {
					if str, ok := v.(string); ok && str != "" {
						out = append(out, str)
					}
				}
				return out, nil
			}
		}
	}
	return []string{}, nil
}

// SyncInboundRouting makes the xray template route inboundTag's traffic
// through outboundTags:
//   - none  → the panel-managed rule/balancer for the inbound is removed
//   - one   → a rule with outboundTag
//   - many  → a balancer over those outbounds, and a rule with balancerTag
//
// It only touches rules/balancers it created itself (identified by ruleTag /
// balancer tag prefixes), so hand-written routing is never modified.
// oldInboundTag may differ from inboundTag when an inbound was renamed; the
// managed entries under the old tag are dropped. Returns true when the
// template changed (and was saved).
func (s *XraySettingService) SyncInboundRouting(oldInboundTag, inboundTag string, outboundTags []string) (bool, error) {
	templateStr, err := s.GetXrayConfigTemplate()
	if err != nil {
		return false, common.NewError("failed to read xray template:", err)
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(templateStr), &template); err != nil {
		return false, common.NewError("xray template is not valid JSON:", err)
	}

	// Normalise + validate the requested outbound tags.
	existing := map[string]bool{}
	outbounds, _ := template["outbounds"].([]any)
	for _, o := range outbounds {
		if om, ok := o.(map[string]any); ok {
			if t, _ := om["tag"].(string); t != "" {
				existing[t] = true
			}
		}
	}
	// Subscription outbounds are injected at runtime, not in the template,
	// but they are valid routing/balancer targets.
	if subTags, err := (&OutboundSubscriptionService{}).AllActiveOutboundTags(); err == nil {
		for _, t := range subTags {
			existing[t] = true
		}
	}

	routing, _ := template["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
	}

	// Pre-existing balancers (not ours) can be picked as a single target.
	userBalancers := map[string]bool{}
	ownBalTags := map[string]bool{
		inboundRouteBalancerTag(inboundTag): true,
		inboundRouteBalancerTag(oldInboundTag): true,
	}
	if bl, ok := routing["balancers"].([]any); ok {
		for _, b := range bl {
			if bm, ok := b.(map[string]any); ok {
				if t, _ := bm["tag"].(string); t != "" && !ownBalTags[t] {
					userBalancers[t] = true
				}
			}
		}
	}

	seen := map[string]bool{}
	wanted := make([]string, 0, len(outboundTags))
	for _, t := range outboundTags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if !existing[t] && !userBalancers[t] {
			return false, common.NewError("unknown outbound tag:", t)
		}
		seen[t] = true
		wanted = append(wanted, t)
	}
	if len(wanted) > 1 {
		for _, t := range wanted {
			if userBalancers[t] && !existing[t] {
				return false, common.NewError("a balancer can only be selected on its own:", t)
			}
		}
	}

	dropTags := map[string]bool{inboundRouteRuleTag(inboundTag): true}
	dropBalancers := map[string]bool{inboundRouteBalancerTag(inboundTag): true}
	if oldInboundTag != "" && oldInboundTag != inboundTag {
		dropTags[inboundRouteRuleTag(oldInboundTag)] = true
		dropBalancers[inboundRouteBalancerTag(oldInboundTag)] = true
	}

	rules, _ := routing["rules"].([]any)
	keptRules := make([]any, 0, len(rules)+1)
	for _, r := range rules {
		if rm, ok := r.(map[string]any); ok {
			if t, _ := rm["ruleTag"].(string); dropTags[t] {
				continue
			}
		}
		keptRules = append(keptRules, r)
	}
	balancers, _ := routing["balancers"].([]any)
	keptBalancers := make([]any, 0, len(balancers)+1)
	for _, b := range balancers {
		if bm, ok := b.(map[string]any); ok {
			if t, _ := bm["tag"].(string); dropBalancers[t] {
				continue
			}
		}
		keptBalancers = append(keptBalancers, b)
	}

	if len(wanted) > 0 && inboundTag != "" {
		rule := map[string]any{
			"type":       "field",
			"ruleTag":    inboundRouteRuleTag(inboundTag),
			"inboundTag": []any{inboundTag},
		}
		if len(wanted) == 1 {
			if userBalancers[wanted[0]] && !existing[wanted[0]] {
				rule["balancerTag"] = wanted[0]
			} else {
				rule["outboundTag"] = wanted[0]
			}
		} else {
			balTag := inboundRouteBalancerTag(inboundTag)
			sel := make([]any, 0, len(wanted))
			for _, t := range wanted {
				sel = append(sel, t)
			}
			keptBalancers = append(keptBalancers, map[string]any{
				"tag":      balTag,
				"selector": sel,
			})
			rule["balancerTag"] = balTag
		}
		// Front of the list so it wins over generic catch-all rules.
		keptRules = append([]any{rule}, keptRules...)
	}

	if len(keptRules) > 0 {
		routing["rules"] = keptRules
	} else {
		delete(routing, "rules")
	}
	if len(keptBalancers) > 0 {
		routing["balancers"] = keptBalancers
	} else {
		delete(routing, "balancers")
	}
	template["routing"] = routing

	newJSON, err := json.MarshalIndent(template, "", "  ")
	if err != nil {
		return false, common.NewError("failed to rebuild xray template:", err)
	}

	// Compare semantically so a no-op sync doesn't rewrite the template.
	var before, after any
	_ = json.Unmarshal([]byte(templateStr), &before)
	_ = json.Unmarshal(newJSON, &after)
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if string(b1) == string(b2) {
		return false, nil
	}

	if err := s.SaveXraySetting(string(newJSON)); err != nil {
		return false, err
	}
	return true, nil
}
