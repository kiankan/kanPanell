package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

const autoInboundRemarkPrefix = "[Auto Inbound] "

type autoInboundTLSRequest struct {
	Domain          string `json:"domain" binding:"required"`
	CertificateFile string `json:"certificateFile" binding:"required"`
	KeyFile         string `json:"keyFile" binding:"required"`
}

type autoInboundResult struct {
	Created int      `json:"created"`
	Removed int      `json:"removed"`
	Skipped []string `json:"skipped"`
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
		jsonMsg(c, "Auto Inbound reconciliation failed", err)
		return
	}
	if needRestart {
		a.XrayService.SetToNeedRestart()
	}
	jsonObj(c, result, nil)
}

func autoInboundTag(outboundTag string) string {
	sum := sha256.Sum256([]byte(outboundTag))
	return "auto-in-" + hex.EncodeToString(sum[:8])
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
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	return tags, nil
}

func (a *XraySettingController) syncAutoInbounds(userID int, cfg service.AutoInboundTLSConfig) (*autoInboundResult, bool, error) {
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
	if subscriptionTags, subErr := a.OutboundSubscriptionService.AllActiveOutboundTags(); subErr == nil {
		seen := make(map[string]bool, len(outboundTags))
		for _, tag := range outboundTags {
			seen[tag] = true
		}
		for _, tag := range subscriptionTags {
			tag = strings.TrimSpace(tag)
			if tag != "" && !seen[tag] {
				outboundTags = append(outboundTags, tag)
				seen[tag] = true
			}
		}
	}
	inbounds, err := a.InboundService.GetInbounds(userID)
	if err != nil {
		return nil, false, err
	}
	managed := map[string]*model.Inbound{}
	usedPorts := map[int]bool{}
	for _, inbound := range inbounds {
		if inbound != nil && inbound.Port > 0 {
			usedPorts[inbound.Port] = true
		}
		if inbound != nil && strings.HasPrefix(inbound.Remark, autoInboundRemarkPrefix) {
			managed[strings.TrimPrefix(inbound.Remark, autoInboundRemarkPrefix)] = inbound
		}
	}
	wanted := map[string]bool{}
	pending := map[string]*model.Inbound{}
	result := &autoInboundResult{Skipped: []string{}}
	needRestart := false

	// Prepare new inbounds and route rules before enabling any new listener.
	// Xray must have the exact inboundTag -> outboundTag rule loaded first;
	// otherwise a hot-added inbound could briefly use the default outbound.
	for _, outboundTag := range outboundTags {
		wanted[outboundTag] = true
		if existing := managed[outboundTag]; existing != nil {
			routeChanged, routeErr := a.XraySettingService.SyncInboundRouting(existing.Tag, existing.Tag, []string{outboundTag})
			if routeErr != nil {
				return result, needRestart, routeErr
			}
			needRestart = needRestart || routeChanged
			delete(managed, outboundTag)
			continue
		}
		port, portErr := allocateAutoInboundPort(usedPorts)
		if portErr != nil {
			result.Skipped = append(result.Skipped, outboundTag+": "+portErr.Error())
			continue
		}
		clientID := uuid.NewString()
		pending[outboundTag] = &model.Inbound{
			UserId:   userID,
			Remark:   autoInboundRemarkPrefix + outboundTag,
			Enable:   true,
			Port:     port,
			Protocol: model.VLESS,
			Tag:      autoInboundTag(outboundTag),
			Settings: marshalAutoInbound(map[string]any{
				"clients":    []any{map[string]any{"id": clientID, "email": autoInboundTag(outboundTag)}},
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

	for outboundTag, inbound := range managed {
		if wanted[outboundTag] {
			continue
		}
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
			return result, true, fmt.Errorf("failed to apply Auto Inbound routes before creating listeners: %w", err)
		}
		needRestart = false
	}

	for outboundTag, inbound := range pending {
		created, restart, addErr := a.InboundService.AddInbound(inbound)
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
