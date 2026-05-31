import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { HealthCheck } from '../api/types';
import JobTable from '../components/JobTable';
import StatusBadge from '../components/StatusBadge';

export default function HealthPage() {
  const [checks, setChecks] = useState<HealthCheck[]>([]);

  useEffect(() => {
    api.getHealth().then(setChecks).catch(() => setChecks([]));
  }, []);

  return (
    <div>
      <h2 style={{ marginTop: 0, color: '#f8fafc', marginBottom: '16px' }}>Health Checks</h2>
      <JobTable
        rows={checks as unknown as Record<string, unknown>[]}
        columns={[
          { key: 'name', label: 'Service', sortable: true },
          {
            key: 'status',
            label: 'Status',
            sortable: true,
            render: (v) => <StatusBadge status={v as HealthCheck['status']} />,
          },
          { key: 'message', label: 'Message', sortable: true },
          { key: 'lastChecked', label: 'Last Checked', sortable: true },
        ]}
      />
    </div>
  );
}
