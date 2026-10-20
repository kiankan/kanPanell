import { fireEvent, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { expect, test, vi } from 'vitest';

import AIAssistantPage from '@/pages/ai-assistant/AIAssistantPage';
import { renderWithProviders } from './test-utils';

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock('@/utils', async (importOriginal) => ({
  // Only HttpUtil is faked; parseMsg and friends still need the real Msg.
  ...(await importOriginal<typeof import('@/utils')>()),
  HttpUtil: {
    get: (...args: unknown[]) => getMock(...args),
    post: (...args: unknown[]) => postMock(...args),
  },
}));

vi.mock('@/layouts/AppSidebar', () => ({ default: () => <div data-testid="sidebar" /> }));
vi.mock('@/hooks/useTheme', async (importOriginal) => ({
  // Keep the real ThemeProvider (renderWithProviders wraps with it); only
  // pin the values the page reads from useTheme().
  ...(await importOriginal<typeof import('@/hooks/useTheme')>()),
  useTheme: () => ({ isDark: true, isUltra: false }),
}));

test('shows diagnostics and does not invoke a repair API after acknowledging the plan', async () => {
  getMock.mockResolvedValue({
    success: true,
    obj: [{ id: 4, name: 'node-4', address: 'https://node.example', enable: true }],
  });
  postMock.mockResolvedValue({
    success: true,
    obj: { status: 'online', latencyMs: 18, xrayState: 'running' },
  });

  renderWithProviders(
    <MemoryRouter>
      <AIAssistantPage />
    </MemoryRouter>,
  );

  await waitFor(() =>
    expect(getMock).toHaveBeenCalledWith('/panel/api/nodes/list', undefined, { silent: true }),
  );
  fireEvent.mouseDown(screen.getByRole('combobox'));
  fireEvent.click(await screen.findByText('node-4 (#4)'));
  fireEvent.click(screen.getByRole('button', { name: /اجرای بررسی از طریق API/ }));

  await waitFor(() => expect(postMock).toHaveBeenCalledWith('/panel/api/nodes/probe/4'));
  expect(await screen.findByText('API پنل نود قابل دسترسی است.')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: /تأیید برنامه بررسی/ }));
  expect(await screen.findByRole('button', { name: /تأیید بررسی ثبت شد/ })).toBeTruthy();
  expect(postMock).toHaveBeenCalledTimes(1);
});

test('does not classify a node with a missing enable field as disabled', async () => {
  getMock.mockResolvedValue({
    success: true,
    obj: [{ id: 5, name: 'node-5', address: 'https://node.example' }],
  });
  postMock.mockResolvedValue({
    success: true,
    obj: { status: 'online', latencyMs: 20 },
  });

  renderWithProviders(
    <MemoryRouter>
      <AIAssistantPage />
    </MemoryRouter>,
  );

  fireEvent.mouseDown(screen.getByRole('combobox'));
  fireEvent.click(await screen.findByText('node-5 (#5)'));
  expect(await screen.findByText('نامشخص')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: /اجرای بررسی از طریق API/ }));

  expect(await screen.findByText('وضعیت فعال‌بودن نود در پاسخ پنل مشخص نیست.')).toBeTruthy();
  expect(screen.queryByText('نود در پنل غیرفعال است.')).toBeNull();
});

test('flags a negated Xray state instead of treating the word running as healthy', async () => {
  getMock.mockResolvedValue({
    success: true,
    obj: [{ id: 6, name: 'node-6', enable: true }],
  });
  postMock.mockResolvedValue({
    success: true,
    obj: { status: 'online', xrayState: 'not running' },
  });

  renderWithProviders(
    <MemoryRouter>
      <AIAssistantPage />
    </MemoryRouter>,
  );

  fireEvent.mouseDown(screen.getByRole('combobox'));
  fireEvent.click(await screen.findByText('node-6 (#6)'));
  fireEvent.click(screen.getByRole('button', { name: /اجرای بررسی از طریق API/ }));

  expect(await screen.findByText('وضعیت Xray نیازمند بررسی است: not running')).toBeTruthy();
  expect(screen.getByText('ERROR')).toBeTruthy();
});

test('warns when a successful probe omits the Xray state', async () => {
  getMock.mockResolvedValue({
    success: true,
    obj: [{ id: 7, name: 'node-7', enable: true }],
  });
  postMock.mockResolvedValue({
    success: true,
    obj: { status: 'online', latencyMs: 21 },
  });

  renderWithProviders(
    <MemoryRouter>
      <AIAssistantPage />
    </MemoryRouter>,
  );

  fireEvent.mouseDown(screen.getByRole('combobox'));
  fireEvent.click(await screen.findByText('node-7 (#7)'));
  fireEvent.click(screen.getByRole('button', { name: /اجرای بررسی از طریق API/ }));

  expect(await screen.findByText('وضعیت Xray در پاسخ Probe مشخص نیست.')).toBeTruthy();
});

test('fails closed when a successful probe response has an invalid payload', async () => {
  getMock.mockResolvedValue({
    success: true,
    obj: [{ id: 8, name: 'node-8', enable: true }],
  });
  postMock.mockResolvedValue({
    success: true,
    obj: { latencyMs: 12 },
  });

  renderWithProviders(
    <MemoryRouter>
      <AIAssistantPage />
    </MemoryRouter>,
  );

  fireEvent.mouseDown(screen.getByRole('combobox'));
  fireEvent.click(await screen.findByText('node-8 (#8)'));
  fireEvent.click(screen.getByRole('button', { name: /اجرای بررسی از طریق API/ }));

  // The failure is surfaced both in the notice and in the evidence list.
  expect(
    (await screen.findAllByText(/nodes\/probe response failed validation/)).length,
  ).toBeGreaterThan(0);
  expect(screen.queryByText('API پنل نود قابل دسترسی است.')).toBeNull();
});
