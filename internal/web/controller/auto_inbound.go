package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

const autoInboundRemarkPrefix = "[Auto Inbound] "

// Serialize reconciliation in-process so simultaneous requests cannot both
// observe a missing inbound and create two listeners for the same outbound.
var autoInboundReconcileMu sync.Mutex

type autoInboundTLSRequest struct {
	Domain          string `json:"domain" binding:"required"`
	CertificateFile string `json:"certificateFile" binding:"required"`
	KeyFile         string `json:"keyFile" binding:"required"`
}

type autoInboundResult struct {
	Created   int      `json:"created"`
	Removed   int      `json:"removed"`
	Skipped   []string `json:"skipped"`
	Restarted bool     `json:"-"`
}

// getAutoInboundTLSConfig returns only the dedicated TLS file paths and domain.
func (a *XraySettingController) getAutoInboundTLSConfig(c *gin.Context) {
	cfg, err := a.SettingService.GetAutoInboundTLSConfig()
	if err != nil {
		jsonMsg(c, "Unable to read Auto Inbound TLS settings", err)
		return
	}
	jsonObj(c, cfg, nil)
}

// reconcileAutoInbounds validates the configured TLS material before touching
// any inbound, then ensures exactly one dedicated VLESS inbound per outbound.
func (a *XraySettingController) reconcileAutoInbounds(c *gin.Context) {
	var req autoInboundTLSRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "Domain, certificate path and private-key path are required", err)
		return
	}
	cfg := service.AutoInboundTLSConfig{
		Domain:          strings.TrimSpace(req.Domain),
		CertificateFile: strings.TrimSpace(req.CertificateFile),
		KeyFile:         strings.TrimSpace(req.KeyFile),
	}
	if err := cfg.Validate(); err != nil {
		jsonMsg(c, "Auto Inbound TLS settings are invalid; no inbound was created", err)
		return
	}
	if err := a.SettingService.SaveAutoInboundTLSConfig(cfg); err != nil {
		jsonMsg(c, "Unable to save Auto Inbound TLS settings", err)
		return
	}
	user := session.GetLoginUser(c)
	result, needRestart, err := a.syncAutoInbounds(user.Id, cfg)
	if err != nil {
		// Some reconciliation failures happen after DB mutations. Ensure Xray
		// applies deletions/route cleanup before returning the error to the UI.
		if restartErr := a.applyAutoInboundRestart(needRestart); restartErr != nil {
			err = fmt.Errorf("%w; failed to apply safety changes: %v", err, restartErr)
		}
		jsonMsg(c, "Auto Inbound reconciliation failed", err)
		return
	}
	if needRestart {
		if a.XrayService.IsXrayRunning() {
			if err := a.XrayService.RestartXray(false); err != nil {
				jsonMsg(c, "Auto Inbound routes were synchronized, but Xray could not be restarted", err)
				return
			}
		} else {
			a.XrayService.SetToNeedRestart()
		}
	}
	jsonObj(c, result, nil)
}

func autoInboundTag(outboundTag string) string {
	sum := sha256.Sum256([]byte(outboundTag))
	return "auto-in-" + hex.EncodeToString(sum[:8])
}

func uniqueAutoInboundTag(outboundTag string, used map[string]bool) string {
	base := autoInboundTag(outboundTag)
	tag := base
	for suffix := 1; used[tag]; suffix++ {
		tag = fmt.Sprintf("%s-%d", base, suffix)
	}
	used[tag] = true
	return tag
}

func outboundTagForInbound(rawConfig, inboundTag string) string {
	var config struct {
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if json.Unmarshal([]byte(rawConfig), &config) != nil {
		return ""
	}
	for _, rule := range config.Routing.Rules {
		for _, tag := range rule.InboundTag {
			if tag == inboundTag && rule.OutboundTag != "" {
				return rule.OutboundTag
			}
		}
	}
	return ""
}

func allocateAutoInboundPort(used map[int]bool) (int, error) {
	// Keep ports selected during this reconciliation reserved in-memory too:
	// Xray is restarted only after the batch, so the OS could otherwise hand
	// the same ephemeral port to multiple generated inbounds.
	for attempt := 0; attempt < 32; attempt++ {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "0.0.0.0:0")
		if err != nil {
			return 0, fmt.Errorf("cannot allocate a free TCP port: %w", err)
		}
		addr, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			_ = listener.Close()
			return 0, fmt.Errorf("cannot determine allocated TCP port")
		}
		port := addr.Port
		_ = listener.Close()
		if !used[port] {
			used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("could not allocate a unique TCP port after 32 attempts")
}

func outboundTagsFromTemplate(raw string) ([]string, error) {
	var template struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &template); err != nil {
		return nil, fmt.Errorf("invalid Xray configuration: %w", err)
	}
	seen := map[string]bool{}
	tags := make([]string, 0, len(template.Outbounds))
	for _, outbound := range template.Outbounds {
		tag := strings.TrimSpace(outbound.Tag)
		if tag == "" {
			continue
		}
		if tag != outbound.Tag {
			return nil, fmt.Errorf("outbound tag %q contains leading or trailing whitespace", outbound.Tag)
		}
		if seen[tag] {
			// Xray routing by tag is ambiguous when two outbounds share a tag.
			// Refuse reconciliation instead of silently routing a managed inbound
			// to whichever duplicate Xray happens to resolve.
			return nil, fmt.Errorf("duplicate outbound tag %q in Xray configuration", tag)
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	return tags, nil
}

func (a *XraySettingController) syncAutoInbounds(userID int, cfg service.AutoInboundTLSConfig) (*autoInboundResult, bool, error) {
	autoInboundReconcileMu.Lock()
	defer autoInboundReconcileMu.Unlock()

	// This guard is intentionally repeated at the point of creation: callers
	// cannot bypass TLS validation by reaching reconciliation through another path.
	if err := cfg.Validate(); err != nil {
		return nil, false, fmt.Errorf("Auto Inbound TLS settings are invalid; no inbound was created: %w", err)
	}
	raw, err := a.SettingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, false, err
	}
	outboundTags, err := outboundTagsFromTemplate(raw)
	if err != nil {
		return nil, false, err
	}
	// Active subscription outbounds are injected into Xray at runtime, so merge
	// their current tags into the same reconciliation set as template outbounds.
	subscriptionTags, err := a.OutboundSubscriptionService.AllActiveOutboundTags()
	if err != nil {
		// Reconciliation may delete stale managed inbounds. If subscription
		// state cannot be read, abort before any mutation rather than treating
		// temporarily unavailable subscription outbounds as deleted.
		return nil, false, fmt.Errorf("cannot read active subscription outbound tags: %w", err)
	}
	seen := make(map[string]bool, len(outboundTags)+len(subscriptionTags))
	for _, tag := range outboundTags {
		seen[tag] = true
	}
	for _, rawTag := range subscriptionTags {
		tag := strings.TrimSpace(rawTag)
		if tag == "" {
			continue
		}
		if tag != rawTag {
			return nil, false, fmt.Errorf("subscription outbound tag %q contains leading or trailing whitespace", rawTag)
		}
		if seen[tag] {
			return nil, false, fmt.Errorf("duplicate outbound tag %q across Xray template and active subscriptions", tag)
		}
		outboundTags = append(outboundTags, tag)
		seen[tag] = true
	}
	inbounds, err := a.InboundService.GetInbounds(userID)
	if err != nil {
		return nil, false, err
	}
	managed := map[string][]*model.Inbound{}
	usedPorts := map[int]bool{}
	usedTags := map[string]bool{}
	for _, inbound := range inbounds {
		if inbound != nil && inbound.Tag != "" {
			usedTags[inbound.Tag] = true
		}
		if inbound != nil && inbound.Port > 0 {
			usedPorts[inbound.Port] = true
		}
		if inbound != nil && (strings.HasPrefix(inbound.Remark, autoInboundRemarkPrefix) || strings.HasPrefix(inbound.Tag, "auto-in-")) {
			outboundTag := ""
			if strings.HasPrefix(inbound.Remark, autoInboundRemarkPrefix) {
				outboundTag = strings.TrimPrefix(inbound.Remark, autoInboundRemarkPrefix)
			} else {
				// If an operator renamed the remark, recover the association from
				// the dedicated inboundTag -> outboundTag routing rule.
				outboundTag = outboundTagForInbound(raw, inbound.Tag)
			}
			if outboundTag == "" {
				outboundTag = "\x00orphan:" + inbound.Tag
			}
			managed[outboundTag] = append(managed[outboundTag], inbound)
		}
	}
	wanted := map[string]bool{}
	pending := map[string]*model.Inbound{}
	result := &autoInboundResult{Skipped: []string{}}
	needRestart := false
	routeRepairInbounds := []*model.Inbound{}

	// Prepare new inbounds and route rules before enabling any new listener.
	// Xray must have the exact inboundTag -> outboundTag rule loaded first;
	// otherwise a hot-added inbound could briefly use the default outbound.
	for _, outboundTag := range outboundTags {
		wanted[outboundTag] = true
		if existingList := managed[outboundTag]; len(existingList) > 0 {
			// Keep one canonical inbound and explicitly clean up every duplicate.
			// A map keyed directly by outboundTag used to overwrite duplicates,
			// leaving the overwritten listeners permanently unmanaged.
			existing := existingList[0]
			routeChanged, routeErr := a.XraySettingService.SyncInboundRouting(existing.Tag, existing.Tag, []string{outboundTag})
			if routeErr != nil {
				// Never leave any listener for this outbound active when its
				// exclusive route cannot be verified or repaired. Remove the
				// canonical inbound and all duplicates before returning.
				var cleanupErrors []string
				for _, unsafeInbound := range existingList {
					_, deleteErr := a.InboundService.DelInbound(unsafeInbound.Id)
					if deleteErr != nil {
						cleanupErrors = append(cleanupErrors, fmt.Sprintf("remove %q: %v", unsafeInbound.Tag, deleteErr))
						continue
					}
					_, routeCleanupErr := a.XraySettingService.SyncInboundRouting(unsafeInbound.Tag, "", []string{})
					if routeCleanupErr != nil {
						cleanupErrors = append(cleanupErrors, fmt.Sprintf("clean route for %q: %v", unsafeInbound.Tag, routeCleanupErr))
					}
					result.Removed++
				}
				if len(cleanupErrors) > 0 {
					return result, true, fmt.Errorf("route repair failed for outbound %q (%v); cleanup was incomplete: %s", outboundTag, routeErr, strings.Join(cleanupErrors, "; "))
				}
				return result, true, fmt.Errorf("removed all managed inbounds for outbound %q because its exclusive route could not be repaired: %w", outboundTag, routeErr)
			}
			needRestart = needRestart || routeChanged
			if routeChanged {
				routeRepairInbounds = append(routeRepairInbounds, existing)
			}
			delete(managed, outboundTag)
			for _, duplicate := range existingList[1:] {
				restart, deleteErr := a.InboundService.DelInbound(duplicate.Id)
				if deleteErr != nil {
					return result, needRestart, fmt.Errorf("cannot remove duplicate Auto Inbound %q for outbound %q: %w", duplicate.Tag, outboundTag, deleteErr)
				}
				needRestart = needRestart || restart
				changed, cleanupErr := a.XraySettingService.SyncInboundRouting(duplicate.Tag, "", []string{})
				if cleanupErr != nil {
					return result, true, fmt.Errorf("removed duplicate Auto Inbound %q but could not clean its route: %w", duplicate.Tag, cleanupErr)
				}
				needRestart = needRestart || changed
				result.Removed++
			}
			continue
		}
		port, portErr := allocateAutoInboundPort(usedPorts)
		if portErr != nil {
			result.Skipped = append(result.Skipped, outboundTag+": "+portErr.Error())
			continue
		}
		clientID := uuid.NewString()
		inboundTag := uniqueAutoInboundTag(outboundTag, usedTags)
		clientEmail := "auto-" + strings.ReplaceAll(clientID, "-", "")
		pending[outboundTag] = &model.Inbound{
			UserId:   userID,
			Remark:   autoInboundRemarkPrefix + outboundTag,
			Enable:   true,
			Port:     port,
			Protocol: model.VLESS,
			Tag:      inboundTag,
			Settings: marshalAutoInbound(map[string]any{
				"clients":    []any{map[string]any{"id": clientID, "email": clientEmail}},
				"decryption": "none",
				"encryption": "none",
			}),
			StreamSettings: marshalAutoInbound(map[string]any{
				"network":  "tcp",
				"security": "tls",
				"tlsSettings": map[string]any{
					"serverName": cfg.Domain,
					"certificates": []any{map[string]any{
						"certificateFile": cfg.CertificateFile,
						"keyFile":         cfg.KeyFile,
					}},
					"alpn": []string{"h2", "http/1.1"},
				},
			}),
			Sniffing: marshalAutoInbound(map[string]any{"enabled": true, "destOverride": []string{"http", "tls"}}),
		}
	}

	for outboundTag, inbounds := range managed {
		if wanted[outboundTag] {
			continue
		}
		for _, inbound := range inbounds {
			// Delete the inbound first so a route-cleanup failure cannot leave an
			// active listener falling through to Xray's default outbound.
			restart, err := a.InboundService.DelInbound(inbound.Id)
			if err != nil {
				return result, needRestart, err
			}
			needRestart = needRestart || restart
			routeChanged, err := a.XraySettingService.SyncInboundRouting(inbound.Tag, "", []string{})
			if err != nil {
				return result, needRestart, err
			}
			needRestart = needRestart || routeChanged
			result.Removed++
		}
	}

	for outboundTag, inbound := range pending {
		routeChanged, routeErr := a.XraySettingService.SyncInboundRouting("", inbound.Tag, []string{outboundTag})
		if routeErr != nil {
			result.Skipped = append(result.Skipped, outboundTag+": "+routeErr.Error())
			delete(pending, outboundTag)
			continue
		}
		needRestart = needRestart || routeChanged
	}

	// Apply all new/updated route rules before hot-adding the matching inbounds.
	if (needRestart || len(pending) > 0) && a.XrayService.IsXrayRunning() {
		if err := a.XrayService.RestartXray(false); err != nil {
			for _, inbound := range routeRepairInbounds {
				if _, deleteErr := a.InboundService.DelInbound(inbound.Id); deleteErr != nil {
					return result, true, fmt.Errorf("route reload failed (%w) and inbound %q could not be removed: %w", err, inbound.Tag, deleteErr)
				}
			}
			return result, true, fmt.Errorf("failed to apply Auto Inbound routes before creating listeners: %w", err)
		}
		result.Restarted = true
		needRestart = false
	}

	for outboundTag, inbound := range pending {
		_, restart, addErr := a.InboundService.AddInbound(inbound)
		if addErr != nil {
			result.Skipped = append(result.Skipped, outboundTag+": "+addErr.Error())
			routeChanged, cleanupErr := a.XraySettingService.SyncInboundRouting(inbound.Tag, "", []string{})
			if cleanupErr != nil {
				return result, true, fmt.Errorf("failed to clean route for uncreated inbound %q: %w", inbound.Tag, cleanupErr)
			}
			needRestart = needRestart || routeChanged
			continue
		}
		needRestart = needRestart || restart
		result.Created++
	}
	return result, needRestart, nil
}

func marshalAutoInbound(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
