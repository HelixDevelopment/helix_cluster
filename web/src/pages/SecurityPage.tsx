import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { SecurityPolicy } from '../api/types';
import JobTable from '../components/JobTable';
import StatusBadge from '../components/StatusBadge';

export default function SecurityPage() {
  const [policies, setPolicies] = useState<SecurityPolicy[]>([]);

  useEffect(() => {
    api.getPolicies().then(setPolicies).catch(() => setPolicies([]));
  }, []);

  return (
    <div>
      <h2 style={{ marginTop: 0, color: '#f8fafc', marginBottom: '16px' }}>Security</h2>
      <JobTable
        rows={policies as unknown as Record<string, unknown>[]}
        columns={[
          { key: 'name', label: 'Policy', sortable: true },
          { key: 'type', label: 'Type', sortable: true },
          {
            key: 'status',
            label: 'Status',
            sortable: true,
            render: (v) => <StatusBadge status={v as SecurityPolicy['status']} />,
          },
          { key: 'updatedAt', label: 'Updated', sortable: true },
        ]}
      />
    </div>
  );
}
