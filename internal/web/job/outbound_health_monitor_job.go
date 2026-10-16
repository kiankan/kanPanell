package job

import "github.com/mhsanaei/3x-ui/v3/internal/web/service"

// OutboundHealthMonitorJob runs persistent outbound health checks independently
// of the browser session. The service keeps the feature disabled until opted in.
type OutboundHealthMonitorJob struct {
	xrayService *service.XrayService
}

func NewOutboundHealthMonitorJob() *OutboundHealthMonitorJob {
	return &OutboundHealthMonitorJob{xrayService: &service.XrayService{}}
}

func (j *OutboundHealthMonitorJob) Run() {
	if j.xrayService == nil {
		j.xrayService = &service.XrayService{}
	}
	j.xrayService.RunPersistentOutboundHealthMonitor()
}
