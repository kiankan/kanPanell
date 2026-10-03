import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Select, type SelectProps } from 'antd';

import { FormField } from '@/components/form/rhf';
import { useOutboundTagGroups } from '@/api/queries/useOutboundTags';

// Panel-managed balancers created by this same feature for OTHER inbounds
// (internal/web/service/inbound_routing.go's inboundRouteBalancerTag). These
// are an implementation detail, not something to offer as a pick-list item.
const MANAGED_BALANCER_PREFIX = 'inbound-route-bal:';

export default function RouteOutboundsField() {
  const { t } = useTranslation();
  // This field renders on essentially every inbound add/edit (all protocols
  // but MTProto), so fetching eagerly on mount would turn every form open
  // into an extra request. Load lazily on first interaction instead; once
  // fetched, react-query's cache (shared with the Xray settings page) keeps
  // it around.
  const [everOpened, setEverOpened] = useState(false);
  const { data } = useOutboundTagGroups({ enabled: everOpened });

  const outboundOptions = (data?.outbounds ?? []).map((tag) => ({ value: tag, label: tag }));
  const balancerOptions = (data?.balancers ?? [])
    .filter((tag) => !tag.startsWith(MANAGED_BALANCER_PREFIX))
    .map((tag) => ({ value: tag, label: tag }));

  const options: SelectProps['options'] =
    balancerOptions.length > 0
      ? [
          {
            label: t('pages.inbounds.form.routeOutboundsGroupOutbounds'),
            options: outboundOptions,
          },
          {
            label: t('pages.inbounds.form.routeOutboundsGroupBalancers'),
            options: balancerOptions,
          },
        ]
      : outboundOptions;

  return (
    <FormField
      name={['routeOutbounds']}
      label={t('pages.inbounds.form.routeOutbounds')}
      tooltip={t('pages.inbounds.form.routeOutboundsHint')}
    >
      <Select
        mode="multiple"
        allowClear
        showSearch
        optionFilterProp="label"
        placeholder={t('pages.inbounds.form.routeOutboundsPlaceholder')}
        options={options}
        onOpenChange={(open) => {
          if (open) setEverOpened(true);
        }}
      />
    </FormField>
  );
}
