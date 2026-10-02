package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
)

// AddOutboundRelayRequest is the input for OutboundService.AddRelay: Config is
// either a share-link (vless://, vmess://, trojan://, ss://,
// hysteria2:///hy2://, wireguard:///wg://) or a raw Xray outbound JSON
// object. Remark is optional and only shapes the generated tag/port/remark.
type AddOutboundRelayRequest struct {
	Config string `json:"config"`
	Remark string `json:"remark"`
	// UserId is the owning admin's id, set by the controller from the
	// session (never trusted from the request body). AddRelay's inbound
	// is otherwise created with UserId 0, which matches no real admin —
	// it's then invisible in GetInbounds' `WHERE user_id = ?` listing.
	UserId int `json:"-"`
}

// AddOutboundRelayResult reports everything AddRelay created: the outbound
// now in the Xray template, the single-member balancer wrapping it (so more
// outbounds can be added under the same balancer tag later for real load
// balancing), and the new local VLESS+TCP+REALITY inbound clients connect to
// so their traffic exits through it. RealityTarget/RealityServerName are the
// live-probed, verified-reachable site the inbound piggybacks on.
type AddOutboundRelayResult struct {
	OutboundTag       string `json:"outboundTag"`
	BalancerTag       string `json:"balancerTag"`
	InboundId         int    `json:"inboundId"`
	InboundTag        string `json:"inboundTag"`
	Port              int    `json:"port"`
	ClientId          string `json:"clientId"`
	RealityPublicKey  string `json:"realityPublicKey"`
	RealityShortId    string `json:"realityShortId"`
	RealityTarget     string `json:"realityTarget"`
	RealityServerName string `json:"realityServerName"`
}

// relayInboundBasePort is the first port tried for a generated relay
// inbound; ports already used by another inbound are skipped.
const relayInboundBasePort = 20000

// AddRelay parses a share-link or raw outbound JSON, adds it to the Xray
// template's outbounds, wraps it in a new single-member balancer, and
// creates a real client-facing VLESS+TCP+REALITY inbound on its own port
// (key pair from the xray binary, destination live-probed as REALITY-
// feasible) with a routing rule sending that inbound's traffic through the
// balancer. The template save and the inbound creation are ordered so a
// failure never leaves an orphaned outbound/balancer/rule with nothing
// routing into it.
func (s *XraySettingService) AddRelay(req AddOutboundRelayRequest) (*AddOutboundRelayResult, error) {
	outboundCfg, identity, err := parseRelayOutboundConfig(req.Config)
	if err != nil {
		return nil, err
	}

	xraySettingSvc := &XraySettingService{}
	templateStr, err := xraySettingSvc.GetXrayConfigTemplate()
	if err != nil {
		return nil, common.NewError("failed to read xray template:", err)
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(templateStr), &template); err != nil {
		return nil, common.NewError("xray template is not valid JSON:", err)
	}

	outbounds, _ := template["outbounds"].([]any)
	existingOutboundTags := map[string]bool{}
	for _, o := range outbounds {
		if om, ok := o.(map[string]any); ok {
			if t, _ := om["tag"].(string); t != "" {
				existingOutboundTags[t] = true
			}
		}
	}

	routing, _ := template["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
	}
	balancers, _ := routing["balancers"].([]any)
	existingBalancerTags := map[string]bool{}
	for _, b := range balancers {
		if bm, ok := b.(map[string]any); ok {
			if t, _ := bm["tag"].(string); t != "" {
				existingBalancerTags[t] = true
			}
		}
	}

	slug := slugifyRelayName(req.Remark, identity)
	outboundTag := uniqueRelayTag("relay-"+slug, existingOutboundTags)
	balancerTag := uniqueRelayTag("relay-balancer-"+slug, existingBalancerTags)
	inboundTag := "relay-in-" + strings.TrimPrefix(outboundTag, "relay-")

	// A pasted JSON outbound may already carry its own tag; ours always
	// wins so the balancer/rule built below stay correct.
	outboundCfg["tag"] = outboundTag
	outbounds = append(outbounds, outboundCfg)
	template["outbounds"] = outbounds

	balancers = append(balancers, map[string]any{
		"tag":      balancerTag,
		"selector": []any{outboundTag},
	})
	routing["balancers"] = balancers

	rules, _ := routing["rules"].([]any)
	rule := map[string]any{
		"type":        "field",
		"inboundTag":  []any{inboundTag},
		"balancerTag": balancerTag,
	}
	routing["rules"] = append([]any{rule}, rules...)
	template["routing"] = routing

	newTemplateJSON, err := json.MarshalIndent(template, "", "  ")
	if err != nil {
		return nil, common.NewError("failed to rebuild xray template:", err)
	}

	port, err := nextFreeInboundPort(relayInboundBasePort)
	if err != nil {
		return nil, err
	}

	reality, err := buildRealityInboundConfig()
	if err != nil {
		return nil, err
	}

	clientId := uuid.NewString()
	remark := req.Remark
	if remark == "" {
		remark = "Relay " + slug
	}

	settingsJSON, err := json.Marshal(map[string]any{
		"clients": []any{
			map[string]any{
				"id":     clientId,
				"email":  remark,
				"flow":   "xtls-rprx-vision",
				"enable": true,
			},
		},
		"decryption": "none",
	})
	if err != nil {
		return nil, common.NewError("failed to build inbound settings:", err)
	}
	streamSettingsJSON, err := json.Marshal(map[string]any{
		"network":  "tcp",
		"security": "reality",
		"realitySettings": map[string]any{
			"show":        false,
			"xver":        0,
			"target":      reality.target,
			"serverNames": []string{reality.serverName},
			"privateKey":  reality.privateKey,
			"shortIds":    []string{reality.shortId},
			"settings": map[string]any{
				"publicKey":   reality.publicKey,
				"fingerprint": "chrome",
				"serverName":  "",
				"spiderX":     "/",
			},
		},
	})
	if err != nil {
		return nil, common.NewError("failed to build inbound stream settings:", err)
	}

	inbound := &model.Inbound{
		UserId:         req.UserId,
		Remark:         remark,
		Enable:         true,
		Port:           port,
		Protocol:       model.VLESS,
		Settings:       string(settingsJSON),
		StreamSettings: string(streamSettingsJSON),
		Tag:            inboundTag,
	}

	// Validate/persist the template first — SaveXraySetting runs its own
	// checks (including against the running core) — and only create the
	// inbound once the template with the new outbound/balancer/rule is
	// known good.
	if err := xraySettingSvc.SaveXraySetting(string(newTemplateJSON)); err != nil {
		return nil, err
	}
	savedInbound, _, err := (&InboundService{}).AddInbound(inbound)
	if err != nil {
		if rollbackErr := xraySettingSvc.SaveXraySetting(templateStr); rollbackErr != nil {
			logger.Warning("relay: failed to roll back xray template after inbound creation failure:", rollbackErr)
		}
		return nil, err
	}

	return &AddOutboundRelayResult{
		OutboundTag:       outboundTag,
		BalancerTag:       balancerTag,
		InboundId:         savedInbound.Id,
		InboundTag:        savedInbound.Tag,
		Port:              port,
		ClientId:          clientId,
		RealityPublicKey:  reality.publicKey,
		RealityShortId:    reality.shortId,
		RealityTarget:     reality.target,
		RealityServerName: reality.serverName,
	}, nil
}

// realityInboundConfig holds the pieces needed to build a VLESS+TCP+REALITY
// inbound: a real key pair from the running xray binary and a destination
// that was just live-probed as REALITY-feasible (TLS 1.3, X25519 key
// exchange, a valid cert chain) — not a hardcoded guess that might be
// blocked, unreachable, or no longer TLS 1.3 by the time a client connects.
type realityInboundConfig struct {
	privateKey string
	publicKey  string
	shortId    string
	target     string
	serverName string
}

// buildRealityInboundConfig generates a REALITY key pair via the actual
// xray binary (`xray x25519` — guarantees a format the running core
// accepts) and picks the best live-probed candidate from the panel's own
// REALITY scanner. It fails loudly rather than falling back to an
// unverified hardcoded target, since an unreachable/non-REALITY-capable
// dest would silently produce an inbound that never completes a handshake.
func buildRealityInboundConfig() (*realityInboundConfig, error) {
	keyPairAny, err := (&ServerService{}).GetNewX25519Cert()
	if err != nil {
		return nil, common.NewError("failed to generate REALITY key pair:", err)
	}
	keyPair, ok := keyPairAny.(map[string]any)
	if !ok {
		return nil, common.NewError("unexpected REALITY key pair format")
	}
	privateKey, _ := keyPair["privateKey"].(string)
	publicKey, _ := keyPair["publicKey"].(string)
	if privateKey == "" || publicKey == "" {
		return nil, common.NewError("REALITY key generation returned an empty key")
	}

	results, err := (&ServerService{}).ScanRealityTargets("")
	if err != nil {
		return nil, common.NewError("failed to scan for a working REALITY target:", err)
	}
	var best *RealityScanResult
	for _, r := range results {
		if r != nil && r.Feasible {
			best = r
			break
		}
	}
	if best == nil {
		return nil, common.NewError("no reachable REALITY-capable target found from this server — check outbound network access, or add/verify candidates under Settings → REALITY scan, then retry")
	}

	serverName := best.Host
	if len(best.ServerNames) > 0 && best.ServerNames[0] != "" {
		serverName = best.ServerNames[0]
	}

	shortId, err := randomHex(8)
	if err != nil {
		return nil, common.NewError("failed to generate REALITY short ID:", err)
	}

	return &realityInboundConfig{
		privateKey: privateKey,
		publicKey:  publicKey,
		shortId:    shortId,
		target:     best.Target,
		serverName: serverName,
	}, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// parseRelayOutboundConfig accepts either a share-link or a raw Xray
// outbound JSON object and returns it as a settings map plus an identity
// string used to derive a readable tag when no remark is given.
func parseRelayOutboundConfig(raw string) (map[string]any, string, error) {
	cfg := strings.TrimSpace(raw)
	if cfg == "" {
		return nil, "", common.NewError("outbound config is empty")
	}
	for _, scheme := range []string{"vmess://", "vless://", "trojan://", "ss://", "hysteria2://", "hy2://", "wireguard://", "wg://"} {
		if strings.HasPrefix(cfg, scheme) {
			res, err := link.ParseLink(cfg)
			if err != nil {
				return nil, "", common.NewError("failed to parse share link:", err)
			}
			return map[string]any(res.Outbound), res.Identity, nil
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		return nil, "", common.NewError("config is neither a recognized share link nor valid JSON:", err)
	}
	protocol, _ := parsed["protocol"].(string)
	if protocol == "" {
		return nil, "", common.NewError("outbound JSON is missing a \"protocol\" field")
	}
	identity, _ := parsed["tag"].(string)
	return parsed, identity, nil
}

var relaySlugDisallowed = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyRelayName(remark, identity string) string {
	base := remark
	if base == "" {
		base = identity
	}
	base = strings.ToLower(strings.TrimSpace(base))
	base = relaySlugDisallowed.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		if suffix, err := randomRelaySecret(); err == nil && len(suffix) >= 6 {
			base = strings.ToLower(suffix[:6])
		} else {
			base = "relay"
		}
	}
	return base
}

func uniqueRelayTag(candidate string, taken map[string]bool) string {
	if !taken[candidate] {
		return candidate
	}
	for i := 2; i < 1000; i++ {
		c := fmt.Sprintf("%s-%d", candidate, i)
		if !taken[c] {
			return c
		}
	}
	return candidate
}

// nextFreeInboundPort returns the lowest port >= base not already used by an
// existing inbound. AddInbound's own port-conflict check is the final
// authority; this just picks a sane starting candidate.
func nextFreeInboundPort(base int) (int, error) {
	var ports []int
	if err := database.GetDB().Model(&model.Inbound{}).Pluck("port", &ports).Error; err != nil {
		return 0, common.NewError("failed to read existing inbound ports:", err)
	}
	used := make(map[int]bool, len(ports))
	for _, p := range ports {
		used[p] = true
	}
	port := base
	for used[port] {
		port++
		if port > 65535 {
			return 0, common.NewError("no free port available for relay inbound")
		}
	}
	return port, nil
}

func randomRelaySecret() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
