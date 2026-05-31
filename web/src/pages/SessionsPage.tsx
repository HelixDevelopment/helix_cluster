import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { Session } from '../api/types';
import JobTable from '../components/JobTable';
import StatusBadge from '../components/StatusBadge';

export default function SessionsPage() {
  const [sessions, setSessions] = useState<Session[]>([]);

  useEffect(() => {
    api.getSessions().then(setSessions).catch(() => setSessions([]));
  }, []);

  return (
    <div>
      <h2 style={{ marginTop: 0, color: '#f8fafc', marginBottom: '16px' }}>Sessions</h2>
      <JobTable
        rows={sessions as unknown as Record<string, unknown>[]}
        columns={[
          { key: 'user', label: 'User', sortable: true },
          { key: 'client', label: 'Client', sortable: true },
          {
            key: 'status',
            label: 'Status',
            sortable: true,
            render: (v) => <StatusBadge status={v as Session['status']} />,
          },
          { key: 'startedAt', label: 'Started', sortable: true },
          { key: 'lastActiveAt', label: 'Last Active', sortable: true },
        ]}
      />
    </div>
  );
}
