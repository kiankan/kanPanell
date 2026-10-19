import { fireEvent, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { expect, test, vi } from 'vitest';

import AIAssistantPage from '@/pages/ai-assistant/AIAssistantPage';
import { renderWithProviders } from './test-utils';

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock('@/utils', () => ({
  HttpUtil: {
    get: (...args: unknown[]) => getMock(...args),
    post: (...args: unknown[]) => postMock(...args),
  },
}));

vi.mock('@/layouts/AppSidebar', () => ({ default: () => <div data-testid="sidebar" /> }));
vi.mock('@/hooks/useTheme', () => ({
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

  await waitFor(() => expect(getMock).toHaveBeenCalledWith('/panel/api/nodes/list', undefined, { silent: true }));
  fireEvent.mouseDown(screen.getByRole('combobox'));
  fireEvent.click(await screen.findByText('node-4 (#4)'));
  fireEvent.click(screen.getByRole('button', { name: 'اجرای بررسی از طریق API' }));

  await waitFor(() => expect(postMock).toHaveBeenCalledWith('/panel/api/nodes/probe/4'));
  expect(await screen.findByText('API پنل نود قابل دسترسی است.')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'تأیید برنامه بررسی' }));
  expect(await screen.findByRole('button', { name: 'تأیید بررسی ثبت شد' })).toBeTruthy();
  expect(postMock).toHaveBeenCalledTimes(1);
});
