import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Divider,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import { HttpUtil } from '@/utils';
import type { XraySettingsValue } from '@/hooks/useXraySetting';

interface Props {
  open: boolean;
  settings: XraySettingsValue | null;
  onClose: () => void;
}

interface ProbeResult {
  tag: string;
  success: boolean;
  delay: number;
  error?: string;
}

interface BalancerStatus {
  tag: string;
  running?: boolean;
  override?: string;
  selected?: string[];
}

interface PersistentMonitorConfig {
  enabled: boolean;
  intervalSeconds: number;
  failureThreshold: number;
  testMode: 'tcp' | 'real';
}

interface PersistentMonitorStatus {
  lastRun?: string;
  lastError?: string;
  healthy?: Record<string, boolean>;
  consecutiveFailures?: Record<string, number>;
  managedOverrides?: Record<string, string>;
}

export default function OutboundHealthCheckModal({ open, settings, onClose }: Props) {
  const [messageApi, contextHolder] = message.useMessage();
  const [balancerTag, setBalancerTag] = useState<string>();
  const [intervalSeconds, setIntervalSeconds] = useState(60);
  const [enabled, setEnabled] = useState(false);
  const [checking, setChecking] = useState(false);
  const [results, setResults] = useState<ProbeResult[]>([]);
  const [lastCheck, setLastCheck] = useState<number>();
  const [managedOverride, setManagedOverride] = useState('');
  const [monitorConfig, setMonitorConfig] = useState<PersistentMonitorConfig>({
    enabled: false,
    intervalSeconds: 60,
    failureThreshold: 3,
    testMode: 'tcp',
  });
  const [monitorStatus, setMonitorStatus] = useState<PersistentMonitorStatus>();
  const [monitorLoading, setMonitorLoading] = useState(false);
  const [monitorSaving, setMonitorSaving] = useState(false);
  const inFlight = useRef(false);
  const checkOnceRef = useRef<() => Promise<void>>(async () => {});
  const settingsRef = useRef(settings);

  useEffect(() => {
    settingsRef.current = settings;
  }, [settings]);

  useEffect(() => {
    if (!open) return;
    setMonitorLoading(true);
    Promise.all([
      HttpUtil.get<PersistentMonitorConfig>(
        '/panel/api/xray/outbound-health/config',
        undefined,
        { silent: true },
      ),
      HttpUtil.get<PersistentMonitorStatus>(
        '/panel/api/xray/outbound-health/status',
        undefined,
        { silent: true },
      ),
    ])
      .then(([configResponse, statusResponse]) => {
        if (configResponse?.success && configResponse.obj) {
          setMonitorConfig({
            enabled: Boolean(configResponse.obj.enabled),
            intervalSeconds: Number(configResponse.obj.intervalSeconds || 60),
            failureThreshold: Number(configResponse.obj.failureThreshold || 3),
            testMode: configResponse.obj.testMode === 'real' ? 'real' : 'tcp',
          });
        } else {
          messageApi.error(configResponse?.msg || 'Could not load persistent monitor settings.');
        }
        if (statusResponse?.success && statusResponse.obj) setMonitorStatus(statusResponse.obj);
      })
      .catch(() => {
        messageApi.error('Could not load persistent outbound health monitor status.');
      })
      .finally(() => setMonitorLoading(false));
  }, [open, messageApi]);

  async function saveMonitorConfig() {
    setMonitorSaving(true);
    try {
      const response = await HttpUtil.post<PersistentMonitorConfig>(
        '/panel/api/xray/outbound-health/config',
        monitorConfig,
        { silent: true },
      );
      if (!response?.success || !response.obj) {
        messageApi.error(response?.msg || 'Could not save persistent monitor settings.');
        return;
      }
      setMonitorConfig(response.obj);
      messageApi.success('Persistent outbound health monitor settings saved.');
      const statusResponse = await HttpUtil.get<PersistentMonitorStatus>(
        '/panel/api/xray/outbound-health/status',
        undefined,
        { silent: true },
      );
      if (statusResponse?.success && statusResponse.obj) setMonitorStatus(statusResponse.obj);
    } catch {
      messageApi.error('Could not save persistent monitor settings.');
    } finally {
      setMonitorSaving(false);
    }
  }

  const balancers = useMemo(
    () => (settings?.routing?.balancers || []).filter((b) => !!b?.tag),
    [settings?.routing?.balancers],
  );
  const activeBalancerTag = balancerTag || balancers[0]?.tag;
  const selectedBalancer = balancers.find((b) => b.tag === activeBalancerTag);
  const candidates = useMemo(() => {
    if (!selectedBalancer) return [];
    const selectors = selectedBalancer.selector || [];
    return (settings?.outbounds || []).filter((outbound) => {
      const tag = outbound?.tag || '';
      if (
        !tag ||
        ['freedom', 'blackhole', 'dns'].includes((outbound.protocol || '').toLowerCase())
      ) {
        return false;
      }
      return selectors.some((prefix) => tag.startsWith(prefix));
    });
  }, [selectedBalancer, settings?.outbounds]);

  function handleClose() {
    setEnabled(false);
    setResults([]);
    setManagedOverride('');
    onClose();
  }

  async function checkOnce() {
    if (inFlight.current || !selectedBalancer || candidates.length === 0) return;
    inFlight.current = true;
    setChecking(true);
    try {
      const statusResponse = await HttpUtil.post<BalancerStatus[]>(
        '/panel/api/xray/balancerStatus',
        { tags: selectedBalancer.tag },
        { silent: true },
      );
      const statusObject = statusResponse?.obj as unknown;
      const live = Array.isArray(statusObject)
        ? statusObject[0]
        : statusObject && typeof statusObject === 'object'
          ? (statusObject as Record<string, BalancerStatus>)[selectedBalancer.tag]
          : undefined;

      const probeResponse = await HttpUtil.post<ProbeResult[]>(
        '/panel/api/xray/testOutbounds',
        {
          outbounds: JSON.stringify(candidates),
          allOutbounds: JSON.stringify(settingsRef.current?.outbounds || []),
          mode: 'http',
        },
        { silent: true },
      );
      if (!probeResponse?.success || !Array.isArray(probeResponse.obj)) {
        throw new Error(probeResponse?.msg || 'Outbound health check failed');
      }
      const checked = probeResponse.obj.map((result, index) => ({
        tag: result.tag || candidates[index]?.tag || `outbound-${index + 1}`,
        success: Boolean(result.success),
        delay: Number(result.delay || 0),
        error: result.error,
      }));
      setResults(checked);
      setLastCheck(Date.now());

      const healthy = new Set(checked.filter((r) => r.success).map((r) => r.tag));
      const firstHealthy = candidates.find((o) => healthy.has(o.tag || ''))?.tag || '';
      if (!firstHealthy) {
        messageApi.warning(
          'All balancer members failed their health checks. No override was changed.',
        );
        return;
      }

      const currentOverride = live?.override || '';
      const currentSelected = live?.selected?.[0] || '';
      // If the preferred (first matching) member has recovered, remove only an
      // override created by this monitor. Never clear a manual override.
      const preferredTag =
        currentSelected && candidates.some((candidate) => candidate.tag === currentSelected)
          ? currentSelected
          : candidates[0]?.tag || '';
      if (managedOverride && healthy.has(preferredTag)) {
        if (currentOverride === managedOverride) {
          const clearResponse = await HttpUtil.post(
            '/panel/api/xray/balancerOverride',
            {
              tag: selectedBalancer.tag,
              target: '',
            },
            { silent: true },
          );
          if (clearResponse?.success) {
            setManagedOverride('');
            messageApi.success(
              `Balancer ${selectedBalancer.tag}: preferred member recovered; ` +
                'automatic override cleared.',
            );
          }
        } else {
          setManagedOverride('');
        }
        return;
      }

      const activeTag = currentOverride || currentSelected || preferredTag;
      if (activeTag && !healthy.has(activeTag)) {
        if (currentOverride && currentOverride !== managedOverride) {
          // Respect an override that was set manually or by another workflow.
          return;
        }
        if (firstHealthy !== currentOverride) {
          const response = await HttpUtil.post(
            '/panel/api/xray/balancerOverride',
            {
              tag: selectedBalancer.tag,
              target: firstHealthy,
            },
            { silent: true },
          );
          if (response?.success) {
            setManagedOverride(firstHealthy);
            messageApi.warning(
              `Balancer ${selectedBalancer.tag}: failed member ${activeTag}; ` +
                `switched to ${firstHealthy}.`,
            );
          } else {
            messageApi.error(
              response?.msg || 'Could not switch the balancer to a healthy outbound.',
            );
          }
        }
      }
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : 'Outbound health check failed');
    } finally {
      inFlight.current = false;
      setChecking(false);
    }
  }

  useEffect(() => {
    checkOnceRef.current = checkOnce;
  });

  useEffect(() => {
    if (!open || !enabled || !selectedBalancer || candidates.length === 0) return;
    void checkOnceRef.current();
    const timer = window.setInterval(
      () => void checkOnceRef.current(),
      Math.max(15, intervalSeconds) * 1000,
    );
    return () => window.clearInterval(timer);
  }, [open, enabled, intervalSeconds, balancerTag, candidates.length, selectedBalancer]);

  return (
    <>
      {contextHolder}
      <Modal
        title="Outbound Health Check & Auto Failover"
        open={open}
        onCancel={handleClose}
        onOk={() => void checkOnce()}
        okText="Check now"
        confirmLoading={checking}
        cancelText="Close"
        width={720}
        destroyOnHidden
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="The browser-session check below runs only while this dialog is open."
          description={
            'The browser-session check uses the existing Xray outbound probe and balancer override APIs. ' +
            'It will not change an override that appears to be manually managed, and it will ' +
            'never switch to a member if every probe fails.'
          }
        />
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Typography.Title level={5} style={{ margin: 0 }}>
            Persistent server-side monitor
          </Typography.Title>
          <Alert
            type="info"
            showIcon
            message="Runs in the panel service, even when this dialog is closed."
            description="This setting is saved on the server. It is independent of the browser-session failover controls below."
          />
          <Space wrap align="center">
            <Typography.Text>Enable persistent monitoring</Typography.Text>
            <Switch
              checked={monitorConfig.enabled}
              loading={monitorLoading}
              onChange={(enabled) => setMonitorConfig((current) => ({ ...current, enabled }))}
            />
            <Typography.Text>Interval (seconds)</Typography.Text>
            <InputNumber
              min={15}
              max={3600}
              value={monitorConfig.intervalSeconds}
              onChange={(value) =>
                setMonitorConfig((current) => ({
                  ...current,
                  intervalSeconds: Math.min(3600, Math.max(15, Number(value) || 60)),
                }))
              }
            />
            <Typography.Text>Failure threshold</Typography.Text>
            <InputNumber
              min={1}
              max={20}
              value={monitorConfig.failureThreshold}
              onChange={(value) =>
                setMonitorConfig((current) => ({
                  ...current,
                  failureThreshold: Math.min(20, Math.max(1, Number(value) || 3)),
                }))
              }
            />
            <Select
              value={monitorConfig.testMode}
              onChange={(testMode: 'tcp' | 'real') =>
                setMonitorConfig((current) => ({ ...current, testMode }))
              }
              options={[
                { label: 'TCP probe', value: 'tcp' },
                { label: 'Real request', value: 'real' },
              ]}
              style={{ minWidth: 130 }}
            />
            <Button type="primary" loading={monitorSaving} onClick={() => void saveMonitorConfig()}>
              Save server settings
            </Button>
          </Space>
          {monitorStatus && (
            <Space direction="vertical" size={2}>
              <Typography.Text type="secondary">
                Last server run:{' '}
                {monitorStatus.lastRun
                  ? new Date(monitorStatus.lastRun).toLocaleString()
                  : 'Not run yet'}
              </Typography.Text>
              {monitorStatus.lastError && (
                <Alert type="error" showIcon message={monitorStatus.lastError} />
              )}
              {Object.keys(monitorStatus.managedOverrides || {}).length > 0 && (
                <Typography.Text>
                  Monitor-managed overrides:{' '}
                  {Object.entries(monitorStatus.managedOverrides || {})
                    .map(([tag, target]) => tag + ' → ' + target)
                    .join(', ')}
                </Typography.Text>
              )}
            </Space>
          )}
          <Divider style={{ margin: '8px 0' }} />
          <Typography.Title level={5} style={{ margin: 0 }}>
            Browser-session health check
          </Typography.Title>
          <div>
            <Typography.Text strong>Balancer</Typography.Text>
            <Select
              style={{ width: '100%', marginTop: 6 }}
              value={activeBalancerTag}
              onChange={(value) => {
                setBalancerTag(value);
                setResults([]);
                setManagedOverride('');
              }}
              options={balancers.map((b) => ({ label: b.tag, value: b.tag }))}
              placeholder="Select a balancer"
              disabled={enabled}
            />
          </div>
          <Space wrap align="center">
            <Typography.Text>Automatic failover while dialog is open</Typography.Text>
            <Switch
              checked={enabled}
              onChange={setEnabled}
              disabled={!selectedBalancer || candidates.length === 0}
            />
            <Typography.Text>Interval (seconds)</Typography.Text>
            <InputNumber
              min={15}
              max={3600}
              value={intervalSeconds}
              onChange={(value) =>
                setIntervalSeconds(Math.min(3600, Math.max(15, Number(value) || 60)))
              }
              disabled={enabled}
            />
            <Tag>{candidates.length} member(s)</Tag>
          </Space>
          {candidates.length === 0 && (
            <Alert type="info" showIcon message="No outbound matches this balancer's selector." />
          )}
          {lastCheck && (
            <Typography.Text type="secondary">
              Last check: {new Date(lastCheck).toLocaleTimeString()}
            </Typography.Text>
          )}
          <Table
            size="small"
            pagination={false}
            rowKey="tag"
            dataSource={results}
            columns={[
              { title: 'Outbound', dataIndex: 'tag', key: 'tag' },
              {
                title: 'Status',
                dataIndex: 'success',
                key: 'status',
                render: (success: boolean) => (
                  <Tag color={success ? 'success' : 'error'}>
                    {success ? 'Healthy' : 'Unreachable'}
                  </Tag>
                ),
              },
              {
                title: 'Delay',
                dataIndex: 'delay',
                key: 'delay',
                render: (delay: number, row: ProbeResult) => (row.success ? `${delay} ms` : '—'),
              },
              { title: 'Error', dataIndex: 'error', key: 'error', ellipsis: true },
            ]}
          />
        </Space>
      </Modal>
    </>
  );
}
