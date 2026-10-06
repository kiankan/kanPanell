import { describe, expect, it } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import {
  getDefaultOutboundTag,
  setDefaultOutboundTag,
  getDefaultOutbounds,
  setDefaultOutbounds,
} from '@/pages/xray/basics/helpers';

function tpl(
  outbounds: Array<{ tag?: string; protocol?: string; settings?: unknown }>,
  rules: Array<{ type: string; outboundTag?: string; ip?: string[]; protocol?: string[] }> = [],
): XraySettingsValue {
  return { outbounds, routing: { rules } } as XraySettingsValue;
}

describe('routing default outbound', () => {
  it('reads first outbound tag', () => {
    expect(
      getDefaultOutboundTag(
        tpl([
          { tag: 'warp', protocol: 'socks' },
          { tag: 'direct', protocol: 'freedom' },
        ]),
      ),
    ).toBe('warp');
    expect(getDefaultOutboundTag(tpl([]))).toBe('direct');
  });

  it('moves existing outbound to first position', () => {
    const tt = tpl([
      { tag: 'direct', protocol: 'freedom' },
      { tag: 'warp', protocol: 'socks' },
      { tag: 'blocked', protocol: 'blackhole' },
    ]);
    setDefaultOutboundTag(tt, 'warp');
    expect(tt.outbounds!.map((o) => o?.tag)).toEqual(['warp', 'direct', 'blocked']);
  });

  it('creates blocked outbound when missing', () => {
    const tt = tpl([{ tag: 'direct', protocol: 'freedom' }]);
    setDefaultOutboundTag(tt, 'blocked');
    expect(tt.outbounds![0]?.tag).toBe('blocked');
    expect(tt.outbounds![0]?.protocol).toBe('blackhole');
  });

  it('does not prune direct when only blocked rules reference an outbound', () => {
    const tt = tpl(
      [
        { tag: 'direct', protocol: 'freedom', settings: { domainStrategy: 'AsIs' } },
        { tag: 'blocked', protocol: 'blackhole' },
        { tag: 'warp', protocol: 'socks' },
      ],
      [
        { type: 'field', ip: ['geoip:private'], outboundTag: 'blocked' },
        { type: 'field', protocol: ['bittorrent'], outboundTag: 'blocked' },
      ],
    );
    setDefaultOutboundTag(tt, 'warp');
    expect(tt.outbounds!.map((o) => o?.tag)).toEqual(['warp', 'direct', 'blocked']);
  });
});

describe('routing default outbounds (multi)', () => {
  it('falls back to the single default when no managed balancer exists', () => {
    const tt = tpl([
      { tag: 'warp', protocol: 'socks' },
      { tag: 'direct', protocol: 'freedom' },
    ]);
    expect(getDefaultOutbounds(tt)).toEqual(['warp']);
  });

  it('one tag behaves exactly like the single-select path (no balancer/rule added)', () => {
    const tt = tpl([
      { tag: 'direct', protocol: 'freedom' },
      { tag: 'warp', protocol: 'socks' },
    ]);
    setDefaultOutbounds(tt, ['warp']);
    expect(tt.outbounds!.map((o) => o?.tag)).toEqual(['warp', 'direct']);
    expect(tt.routing?.balancers ?? []).toHaveLength(0);
    expect(tt.routing?.rules ?? []).toHaveLength(0);
    expect(getDefaultOutbounds(tt)).toEqual(['warp']);
  });

  it('several tags build a managed balancer + trailing catch-all rule', () => {
    const tt = tpl([
      { tag: 'direct', protocol: 'freedom' },
      { tag: 'warp', protocol: 'socks' },
      { tag: 'relay-1', protocol: 'vless' },
    ]);
    setDefaultOutbounds(tt, ['warp', 'relay-1']);

    const balancer = tt.routing!.balancers!.find((b) => b?.tag === 'default-outbound-balancer');
    expect(balancer?.selector).toEqual(['warp', 'relay-1']);

    const rules = tt.routing!.rules!;
    expect(rules).toHaveLength(1);
    expect(rules[0]).toMatchObject({
      ruleTag: 'default-outbound-rule',
      balancerTag: 'default-outbound-balancer',
    });
    // No inboundTag/conditions at all: a true, unconditional catch-all.
    expect(rules[0].inboundTag).toBeUndefined();

    expect(getDefaultOutbounds(tt)).toEqual(['warp', 'relay-1']);
  });

  it('existing rules keep precedence: the catch-all is appended, not prepended', () => {
    const tt = tpl(
      [
        { tag: 'direct', protocol: 'freedom' },
        { tag: 'warp', protocol: 'socks' },
        { tag: 'relay-1', protocol: 'vless' },
      ],
      [{ type: 'field', protocol: ['bittorrent'], outboundTag: 'blocked' }],
    );
    setDefaultOutbounds(tt, ['warp', 'relay-1']);
    const rules = tt.routing!.rules!;
    expect(rules).toHaveLength(2);
    expect(rules[0]).toMatchObject({ outboundTag: 'blocked' });
    expect(rules[1]).toMatchObject({ ruleTag: 'default-outbound-rule' });
  });

  it('going from several back to one tag removes the managed balancer/rule', () => {
    const tt = tpl([
      { tag: 'direct', protocol: 'freedom' },
      { tag: 'warp', protocol: 'socks' },
      { tag: 'relay-1', protocol: 'vless' },
    ]);
    setDefaultOutbounds(tt, ['warp', 'relay-1']);
    setDefaultOutbounds(tt, ['relay-1']);

    expect(tt.routing?.balancers ?? []).toHaveLength(0);
    expect(tt.routing?.rules ?? []).toHaveLength(0);
    expect(getDefaultOutbounds(tt)).toEqual(['relay-1']);
    expect(tt.outbounds![0]?.tag).toBe('relay-1');
  });

  it('re-picking several tags replaces the previous managed balancer instead of duplicating it', () => {
    const tt = tpl([
      { tag: 'direct', protocol: 'freedom' },
      { tag: 'warp', protocol: 'socks' },
      { tag: 'relay-1', protocol: 'vless' },
      { tag: 'relay-2', protocol: 'vless' },
    ]);
    setDefaultOutbounds(tt, ['warp', 'relay-1']);
    setDefaultOutbounds(tt, ['relay-1', 'relay-2']);

    expect(tt.routing!.balancers!).toHaveLength(1);
    expect(tt.routing!.rules!).toHaveLength(1);
    expect(tt.routing!.balancers![0].selector).toEqual(['relay-1', 'relay-2']);
  });
});
