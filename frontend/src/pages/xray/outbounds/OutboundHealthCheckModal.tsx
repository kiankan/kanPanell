import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
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

export default function OutboundHealthCheckModal({ open, settings, onClose }: Props) {
  const [messageApi, contextHolder] = message.useMessage();
  const [balancerTag, setBalancerTag] = useState<string>();
  const [intervalSeconds, setIntervalSeconds] = useState(60);
  const [enabled, setEnabled] = useState(false);
  const [checking, setChecking] = useState(false);
  const [results, setResults] = useState<ProbeResult[]>([]);
  const [lastCheck, setLastCheck] = useState<number>();
  const [managedOverride, setManagedOverride] = useState('');
  const inFlight = useRef(false);
  const checkOnceRef = useRef<() => Promise<void>>(async () => {});
  const settingsRef = useRef(settings);

  useEffect(() => {
    settingsRef.current = settings;
  }, [settings]);

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
      const preferredTag = candidates[0]?.tag || '';
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
      messageApi.error(
        error instanceof Error ? error.message : 'Outbound health check failed',
      );
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
          message="Automatic checks run only while this dialog is open."
          description={
            'This monitor uses the existing Xray outbound probe and balancer override APIs. ' +
            'It will not change an override that appears to be manually managed, and it will ' +
            'never switch to a member if every probe fails.'
          }
        />
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
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
            <Typography.Text>Automatic failover</Typography.Text>
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
                render: (delay: number, row: ProbeResult) =>
                  row.success ? `${delay} ms` : '—',
              },
              { title: 'Error', dataIndex: 'error', key: 'error', ellipsis: true },
            ]}
          />
        </Space>
      </Modal>
    </>
  );
}
