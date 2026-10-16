import { useEffect, useState } from 'react';
import { Alert, Form, Input, Modal, message } from 'antd';
import { HttpUtil } from '@/utils';

interface AutoInboundSettingsModalProps {
  open: boolean;
  onClose: () => void;
}

interface AutoInboundTLSConfig {
  domain?: string;
  certificateFile?: string;
  keyFile?: string;
}

export default function AutoInboundSettingsModal({ open, onClose }: AutoInboundSettingsModalProps) {
  const [form] = Form.useForm<AutoInboundTLSConfig>();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [result, setResult] = useState('');

  useEffect(() => {
    if (!open) return;
    HttpUtil.get('/panel/api/xray/auto-inbounds/config')
      .then((r) => {
        if (r?.success && r.obj) form.setFieldsValue(r.obj);
      })
      .catch(() => {
        message.error('Could not load Auto Inbound TLS settings.');
      })
      .finally(() => setLoading(false));
  }, [open, form]);

  async function submit(values: AutoInboundTLSConfig) {
    setSaving(true);
    setResult('');
    try {
      const r = await HttpUtil.post('/panel/api/xray/auto-inbounds/reconcile', values);
      if (!r?.success) {
        message.error(r?.msg || 'Auto Inbound setup failed. No new inbound was created.');
        return;
      }
      const created = Number(r.obj?.created || 0);
      const removed = Number(r.obj?.removed || 0);
      const skipped = Array.isArray(r.obj?.skipped) ? r.obj.skipped : [];
      setResult(`Created: ${created}; removed: ${removed}; skipped: ${skipped.length}.`);
      if (skipped.length) message.warning(`Some outbounds were skipped: ${skipped.join('; ')}`);
      else message.success('Auto Inbound configuration validated and synchronized.');
    } catch {
      message.error('Auto Inbound setup failed. Check the domain and certificate files.');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      title="Auto Inbound — dedicated VLESS/TLS"
      open={open}
      onCancel={onClose}
      onOk={() => form.submit()}
      okText="Validate & synchronize"
      cancelText="Close"
      confirmLoading={saving}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) setResult('');
        if (!visible) {
          form.resetFields();
          setResult('');
        }
      }}
    >
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 16 }}
        message="No inbound is created unless the certificate and private key are readable, match each other, are currently valid, and cover the domain."
        description="Use absolute paths on the panel server. The domain must resolve to this server and the generated inbound ports must be reachable."
      />
      <Form form={form} layout="vertical" onFinish={submit} disabled={loading || saving}>
        <Form.Item
          name="domain"
          label="TLS domain"
          rules={[
            { required: true, message: 'Enter a domain covered by the certificate.' },
            { type: 'string', whitespace: true },
          ]}
        >
          <Input placeholder="vpn.example.com" autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="certificateFile"
          label="Certificate file path"
          rules={[
            { required: true, whitespace: true, message: 'Enter the certificate file path.' },
          ]}
        >
          <Input
            placeholder="/etc/letsencrypt/live/vpn.example.com/fullchain.pem"
            autoComplete="off"
          />
        </Form.Item>
        <Form.Item
          name="keyFile"
          label="Private key file path"
          rules={[
            { required: true, whitespace: true, message: 'Enter the private key file path.' },
          ]}
        >
          <Input
            placeholder="/etc/letsencrypt/live/vpn.example.com/privkey.pem"
            autoComplete="off"
          />
        </Form.Item>
      </Form>
      {result && <Alert type="success" showIcon style={{ marginTop: 12 }} message={result} />}
    </Modal>
  );
}
