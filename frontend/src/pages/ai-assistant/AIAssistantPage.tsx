import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Input,
  Row,
  Select,
  Space,
  Spin,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  CloudServerOutlined,
  ExperimentOutlined,
  SafetyOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';

import { HttpUtil } from '@/utils';
import AppSidebar from '@/layouts/AppSidebar';
import { useTheme } from '@/hooks/useTheme';
import './AIAssistantPage.css';

const { Title, Paragraph, Text } = Typography;

type NodeRecord = {
  id: number;
  name?: string;
  remark?: string;
  address?: string;
  enable?: boolean;
  status?: string;
  xrayState?: string;
  xrayError?: string;
};

type ProbeResult = {
  status?: string;
  latencyMs?: number;
  error?: string;
  xrayState?: string;
  xrayError?: string;
};

type Diagnostic = {
  nodeId: number;
  nodeName: string;
  observedAt: string;
  severity: 'info' | 'warning' | 'error';
  findings: string[];
  plan: string[];
  evidence: string[];
  approved: boolean;
};

type ApiMessage<T> = { success?: boolean; msg?: string; obj?: T };

function analyzeNode(node: NodeRecord, probe?: ProbeResult): Omit<Diagnostic, 'observedAt' | 'approved'> {
  const findings: string[] = [];
  const plan: string[] = [];
  const evidence: string[] = [];
  let severity: Diagnostic['severity'] = 'info';

  if (!node.enable) {
    findings.push('نود در پنل غیرفعال است.');
    plan.push('پس از بررسی وابستگی‌ها، فعال‌سازی نود را با تأیید جداگانه پیشنهاد کنید.');
    severity = 'warning';
  }
  if (probe?.status !== 'online') {
    findings.push(probe?.error || 'پاسخ موفق از API نود دریافت نشده است.');
    plan.push('آدرس، دسترسی شبکه و اعتبارنامه اتصال نود را بررسی کنید؛ تغییری اعمال نشود.');
    severity = 'error';
  } else {
    findings.push('API پنل نود قابل دسترسی است.');
    evidence.push(`زمان پاسخ API: ${typeof probe.latencyMs === 'number' ? probe.latencyMs + ' ms' : 'نامشخص'}`);
  }
  if (probe?.xrayState && !/running|online|started/i.test(probe.xrayState)) {
    findings.push(`وضعیت Xray نیازمند بررسی است: ${probe.xrayState}`);
    if (probe.xrayError) findings.push(`خطای گزارش‌شده: ${probe.xrayError}`);
    plan.push('گزارش سرویس Xray و اعتبار پیکربندی را از API مجاز جمع‌آوری و پیش‌نمایش تعمیر تهیه کنید.');
    severity = 'error';
  }
  if (findings.length === 0) findings.push('مشکل مشخصی از داده‌های در دسترس تشخیص داده نشد.');
  if (plan.length === 0) plan.push('هیچ تغییری لازم نیست؛ پایش وضعیت ادامه یابد.');
  evidence.push(`نود: ${node.name || node.remark || 'Node'} (#${node.id})`);
  evidence.push(`آدرس گزارش‌شده: ${node.address || 'ثبت نشده'}`);
  evidence.push(`وضعیت Xray در پنل: ${node.xrayState || 'نامشخص'}`);
  return { nodeId: node.id, nodeName: node.name || node.remark || `Node #${node.id}`, severity, findings, plan, evidence };
}

export default function AIAssistantPage() {
  const { isDark, isUltra } = useTheme();
  const [selectedNodeId, setSelectedNodeId] = useState<number | undefined>();
  const [question, setQuestion] = useState('');
  const [diagnostic, setDiagnostic] = useState<Diagnostic | null>(null);
  const [approved, setApproved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const { data: nodes = [], isLoading, isError, error, refetch } = useQuery({
    queryKey: ['ai-assistant', 'nodes'],
    queryFn: async () => {
      const response = await HttpUtil.get<ApiMessage<NodeRecord[]>>('/panel/api/nodes/list', undefined, { silent: true });
      if (!response?.success) throw new Error(response?.msg || 'Failed to fetch nodes');
      if (!Array.isArray(response.obj)) throw new Error('Invalid nodes response');
      return response.obj;
    },
    staleTime: 30_000,
  });

  const selectedNode = useMemo(
    () => nodes.find((node) => node.id === selectedNodeId),
    [nodes, selectedNodeId],
  );

  async function runDiagnostics() {
    if (!selectedNode) {
      message.warning('ابتدا یک نود انتخاب کنید.');
      return;
    }
    setBusy(true);
    setDiagnostic(null);
    setApproved(false);
    setNotice(null);
    try {
      const response = await HttpUtil.post<ApiMessage<ProbeResult>>(`/panel/api/nodes/probe/${selectedNode.id}`);
      const probe = response?.success
        ? response.obj
        : { status: 'error', error: response?.msg || 'API بررسی نود پاسخ موفق نداد.' };
      const result = analyzeNode(selectedNode, probe);
      setDiagnostic({ ...result, observedAt: new Date().toISOString(), approved: false });
      setNotice('تحلیل بر اساس داده‌های قابل‌دسترسی پنل و پاسخ Probe انجام شد. هیچ تغییری روی نود اعمال نشده است.');
      await refetch();
    } catch (error) {
      const detail = error instanceof Error ? error.message : 'درخواست بررسی نود ناموفق بود.';
      const result = analyzeNode(selectedNode, { status: 'unreachable', error: detail });
      setDiagnostic({ ...result, observedAt: new Date().toISOString(), approved: false });
      setNotice(`جمع‌آوری اطلاعات به خطا خورد: ${detail}`);
    } finally {
      setBusy(false);
    }
  }

  function approvePlan() {
    // This first version records operator acknowledgement only. It deliberately
    // does not execute an operation until a server-side allowlisted repair API
    // and its audit/approval contract exist.
    if (!diagnostic) return;
    setApproved(true);
    setDiagnostic({ ...diagnostic, approved: true });
    setNotice('تأیید ثبت شد، اما اقدامی اجرا نشد. اجرای تعمیر تا زمان وجود API امن و allowlist سمت سرور غیرفعال است.');
  }

  return (
    <div className={`ai-assistant-page ${isDark ? 'is-dark' : ''} ${isUltra ? 'is-ultra' : ''}`}>
      <AppSidebar />
      <main className="ai-assistant-content">
        <div className="ai-assistant-heading">
          <div>
            <Title level={2}>مرکز هوش مصنوعی</Title>
            <Paragraph type="secondary">تحلیل شفاف نودها، جمع‌آوری شواهد و نمایش برنامه تعمیر قبل از هر اقدام.</Paragraph>
          </div>
          <Tag color="blue" icon={<SafetyOutlined />}>تعمیر خودکار غیرفعال</Tag>
        </div>

        <Alert
          showIcon
          type="info"
          message="حریم دسترسی و ایمنی"
          description="این نسخه فقط از API پنل نود استفاده می‌کند. هیچ SSH، shell یا فرمان دلخواهی اجرا نمی‌شود و تأیید برنامه در این نسخه اقدامی روی سرور اعمال نمی‌کند."
          style={{ marginBottom: 20 }}
        />

        <Row gutter={[16, 16]}>
          <Col xs={24} lg={14}>
            <Card title={<Space><ExperimentOutlined /> عیب‌یابی نود</Space>} className="ai-assistant-card">
              {isError && (
                <Alert
                  showIcon
                  type="error"
                  message="دریافت فهرست نودها ناموفق بود"
                  description={error instanceof Error ? error.message : 'خطای نامشخص در API پنل'}
                  action={<Button size="small" onClick={() => void refetch()}>تلاش دوباره</Button>}
                  style={{ marginBottom: 16 }}
                />
              )}
              <Spin spinning={isLoading}>
                <Space direction="vertical" size="middle" style={{ width: '100%' }}>
                  <Text strong>نود موردنظر</Text>
                  <Select
                    showSearch
                    optionFilterProp="label"
                    placeholder="نود را انتخاب کنید"
                    value={selectedNodeId}
                    onChange={(value) => {
                      setSelectedNodeId(value);
                      setDiagnostic(null);
                      setApproved(false);
                    }}
                    options={nodes.map((node) => ({ value: node.id, label: `${node.name || node.remark || 'Node'} (#${node.id})` }))}
                    style={{ width: '100%' }}
                    notFoundContent="نودی یافت نشد"
                  />
                  {selectedNode && (
                    <Descriptions size="small" column={1} bordered>
                      <Descriptions.Item label="نام">{selectedNode.name}</Descriptions.Item>
                      <Descriptions.Item label="آدرس">{selectedNode.address || 'ثبت نشده'}</Descriptions.Item>
                      <Descriptions.Item label="وضعیت">{selectedNode.enable ? 'فعال' : 'غیرفعال یا نامشخص'}</Descriptions.Item>
                    </Descriptions>
                  )}
                  <Button type="primary" icon={<ThunderboltOutlined />} onClick={runDiagnostics} loading={busy} disabled={!selectedNode || isLoading || isError}>
                    اجرای بررسی از طریق API
                  </Button>
                </Space>
              </Spin>
            </Card>
          </Col>
          <Col xs={24} lg={10}>
            <Card title={<Space><CloudServerOutlined /> دستیار هوشمند</Space>} className="ai-assistant-card">
              <Paragraph type="secondary">
                پرسش خود را درباره وضعیت نود یا خطای مشاهده‌شده بنویسید. اتصال به ارائه‌دهنده مدل زبانی در این تغییر اضافه نشده و این بخش فعلاً نقش میزکار تحلیل و گزارش را دارد.
              </Paragraph>
              <Input.TextArea
                value={question}
                onChange={(event) => setQuestion(event.target.value)}
                placeholder="مثلاً: چرا API نود در دسترس نیست؟"
                autoSize={{ minRows: 3, maxRows: 6 }}
                maxLength={2000}
                showCount
              />
              <Button
                style={{ marginTop: 12 }}
                onClick={() => {
                  setNotice(question.trim()
                    ? 'برای پاسخ مبتنی بر مدل زبانی، ابتدا باید ارائه‌دهنده و کلید API امن سمت سرور پیکربندی شوند. پرسش شما در این نسخه به سرویس خارجی ارسال نشده است.'
                    : 'ابتدا پرسش را وارد کنید.');
                }}
                disabled={!question.trim()}
              >
                تحلیل پرسش
              </Button>
            </Card>
          </Col>
        </Row>

        {diagnostic && (
          <Card
            title="یافته‌ها و برنامه تعمیر پیشنهادی"
            className="ai-assistant-card"
            style={{ marginTop: 16 }}
            extra={<Tag color={diagnostic.severity === 'error' ? 'red' : diagnostic.severity === 'warning' ? 'orange' : 'green'}>{diagnostic.severity.toUpperCase()}</Tag>}
          >
            <Alert
              type="warning"
              showIcon
              message="پیش‌نمایش فقط؛ هیچ تغییری اعمال نمی‌شود"
              description="پس از بررسی علت و گام‌های پیشنهادی می‌توانید تأیید بررسی را ثبت کنید. این دکمه عملیات تعمیر را اجرا نمی‌کند."
              style={{ marginBottom: 16 }}
            />
            <Title level={5}>علت‌ها و یافته‌های احتمالی</Title>
            <ul>{diagnostic.findings.map((finding, index) => <li key={index}>{finding}</li>)}</ul>
            <Title level={5}>برنامه پیشنهادی</Title>
            <ol>{diagnostic.plan.map((step, index) => <li key={index}>{step}</li>)}</ol>
            <Title level={5}>شواهد</Title>
            <ul>{diagnostic.evidence.map((item, index) => <li key={index}>{item}</li>)}</ul>
            <Text type="secondary">زمان ثبت: {new Date(diagnostic.observedAt).toLocaleString()}</Text>
            <div style={{ marginTop: 16 }}>
              <Button type="primary" icon={<CheckCircleOutlined />} onClick={approvePlan} disabled={approved}>
                {approved ? 'تأیید بررسی ثبت شد' : 'تأیید برنامه بررسی'}
              </Button>
            </div>
          </Card>
        )}

        {notice && <Alert style={{ marginTop: 16 }} showIcon type="info" message={notice} closable onClose={() => setNotice(null)} />}
      </main>
    </div>
  );
}
