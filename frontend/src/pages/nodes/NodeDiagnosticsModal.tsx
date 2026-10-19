import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Descriptions,
  Divider,
  List,
  Modal,
  Space,
  Spin,
  Tag,
  Typography,
} from 'antd';
import { ReloadOutlined, ToolOutlined } from '@ant-design/icons';

import { keys } from '@/api/queryKeys';
import type { NodeRecord } from '@/api/queries/useNodesQuery';
import { HttpUtil } from '@/utils';

interface DiagnosticFinding {
  code: string;
  severity: 'error' | 'warning' | 'info';
  title: string;
  detail: string;
  recommendation: string;
}

interface RepairAction {
  id: string;
  label: string;
  description: string;
}

interface DiagnosticReport {
  nodeId: number;
  nodeName: string;
  checkedAt: string;
  status: string;
  xrayState?: string;
  xrayError?: string;
  inboundCount: number;
  inboundStatus: string;
  findings: DiagnosticFinding[];
  repairActions: RepairAction[];
}

interface RepairResult {
  actionId: string;
  accepted: boolean;
  verified: boolean;
  message: string;
  before: string;
  after?: string;
  error?: string;
}

interface NodeDiagnosticsModalProps {
  open: boolean;
  node: NodeRecord | null;
  onClose: () => void;
  onRepaired: () => void;
}

export default function NodeDiagnosticsModal({
  open,
  node,
  onClose,
  onRepaired,
}: NodeDiagnosticsModalProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [confirmAction, setConfirmAction] = useState<RepairAction | null>(null);
  const [repairResult, setRepairResult] = useState<RepairResult | null>(null);

  const {
    data: report,
    isFetching: loading,
    error: diagnosticError,
    refetch: diagnose,
  } = useQuery({
    queryKey: ['nodes', 'diagnostics', node?.id],
    enabled: open && !!node?.id,
    retry: false,
    queryFn: async () => {
      if (!node?.id) throw new Error(t('pages.nodes.diagnostics.failed'));
      const response = await HttpUtil.post<DiagnosticReport>(
        `/panel/api/nodes/diagnose/${node.id}`,
      );
      if (!response?.success || !response.obj) {
        throw new Error(response?.msg || t('pages.nodes.diagnostics.failed'));
      }
      return response.obj;
    },
  });

  const repairMutation = useMutation({
    mutationFn: async () => {
      if (!node?.id || !confirmAction) {
        throw new Error(t('pages.nodes.diagnostics.repairFailed'));
      }
      const response = await HttpUtil.post<RepairResult>(
        `/panel/api/nodes/repair/${node.id}`,
        { actionId: confirmAction.id },
      );
      if (!response?.success || !response.obj) {
        throw new Error(response?.msg || t('pages.nodes.diagnostics.repairFailed'));
      }
      return response.obj;
    },
    onSuccess: async (result) => {
      setRepairResult(result);
      setConfirmAction(null);
      await queryClient.invalidateQueries({ queryKey: ['nodes', 'diagnostics', node?.id] });
      await queryClient.invalidateQueries({ queryKey: keys.nodes.root() });
      onRepaired();
    },
  });

  const errorMessage =
    (diagnosticError instanceof Error ? diagnosticError.message : '') ||
    (repairMutation.error instanceof Error ? repairMutation.error.message : '');

  return (
    <>
      <Modal
        open={open}
        title={t('pages.nodes.diagnostics.title', { name: node?.name || '' })}
        onCancel={onClose}
        footer={
          <Space>
            <Button onClick={onClose}>{t('close')}</Button>
            <Button
              icon={<ReloadOutlined />}
              onClick={() => {
                setRepairResult(null);
                void diagnose();
              }}
              loading={loading}
            >
              {t('pages.nodes.diagnostics.runAgain')}
            </Button>
          </Space>
        }
        width={720}
        destroyOnHidden
      >
        <Typography.Paragraph type="secondary">
          {t('pages.nodes.diagnostics.intro')}
        </Typography.Paragraph>
        {errorMessage && (
          <Alert type="error" showIcon message={errorMessage} style={{ marginBottom: 16 }} />
        )}
        <Spin spinning={loading}>
          {report && (
            <>
              <Descriptions size="small" bordered column={2}>
                <Descriptions.Item label={t('pages.nodes.diagnostics.panelApi')}>
                  <Tag color={report.status === 'online' ? 'success' : 'error'}>
                    {t(`pages.nodes.statusValues.${report.status || 'unknown'}`)}
                  </Tag>
                </Descriptions.Item>
                <Descriptions.Item label={t('pages.nodes.diagnostics.xray')}>
                  <Tag color={report.xrayState === 'running' ? 'success' : 'warning'}>
                    {report.xrayState || t('pages.nodes.statusValues.unknown')}
                  </Tag>
                </Descriptions.Item>
                <Descriptions.Item label={t('pages.nodes.diagnostics.inbounds')} span={2}>
                  {report.inboundStatus === 'ok'
                    ? t('pages.nodes.diagnostics.inboundCount', { count: report.inboundCount })
                    : t('pages.nodes.diagnostics.inboundUnavailable')}
                </Descriptions.Item>
              </Descriptions>
              <Divider orientation="left">{t('pages.nodes.diagnostics.findings')}</Divider>
              <List
                dataSource={report.findings || []}
                locale={{ emptyText: t('pages.nodes.diagnostics.noFindings') }}
                renderItem={(finding) => (
                  <List.Item>
                    <Space direction="vertical" size={4} style={{ width: '100%' }}>
                      <Space wrap>
                        <Tag
                          color={
                            finding.severity === 'error'
                              ? 'error'
                              : finding.severity === 'warning'
                                ? 'warning'
                                : 'blue'
                          }
                        >
                          {t(`pages.nodes.diagnostics.severity.${finding.severity}`)}
                        </Tag>
                        <Typography.Text strong>{finding.title}</Typography.Text>
                      </Space>
                      <Typography.Text>{finding.detail}</Typography.Text>
                      <Typography.Text type="secondary">
                        {t('pages.nodes.diagnostics.recommendation')}: {finding.recommendation}
                      </Typography.Text>
                    </Space>
                  </List.Item>
                )}
              />
              {report.repairActions?.length > 0 && (
                <>
                  <Divider orientation="left">{t('pages.nodes.diagnostics.repairPlan')}</Divider>
                  {report.repairActions.map((action) => (
                    <Alert
                      key={action.id}
                      type="warning"
                      showIcon
                      icon={<ToolOutlined />}
                      message={action.label}
                      description={
                        <Space direction="vertical" style={{ width: '100%' }}>
                          <span>{action.description}</span>
                          <Button
                            type="primary"
                            danger
                            onClick={() => setConfirmAction(action)}
                            disabled={repairMutation.isPending}
                          >
                            {t('pages.nodes.diagnostics.reviewAndApply')}
                          </Button>
                        </Space>
                      }
                      style={{ marginBottom: 12 }}
                    />
                  ))}
                </>
              )}
              {repairResult && (
                <Alert
                  type={repairResult.verified ? 'success' : 'warning'}
                  showIcon
                  message={repairResult.message}
                  description={[
                    `${t('pages.nodes.diagnostics.before')}: ${repairResult.before || '-'}`,
                    `${t('pages.nodes.diagnostics.after')}: ${repairResult.after || '-'}`,
                    repairResult.error || '',
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                  style={{ marginTop: 12 }}
                />
              )}
            </>
          )}
        </Spin>
      </Modal>
      <Modal
        open={!!confirmAction}
        title={t('pages.nodes.diagnostics.confirmTitle')}
        okText={t('pages.nodes.diagnostics.confirmRepair')}
        cancelText={t('cancel')}
        okButtonProps={{ danger: true, loading: repairMutation.isPending }}
        onCancel={() => {
          if (!repairMutation.isPending) setConfirmAction(null);
        }}
        onOk={() => repairMutation.mutate()}
        destroyOnHidden
      >
        <Alert
          type="warning"
          showIcon
          message={confirmAction?.label}
          description={confirmAction?.description}
        />
        <Typography.Paragraph style={{ marginTop: 12 }}>
          {t('pages.nodes.diagnostics.confirmHint')}
        </Typography.Paragraph>
      </Modal>
    </>
  );
}
