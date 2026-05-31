import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { Build } from '../api/types';
import JobTable from '../components/JobTable';
import StatusBadge from '../components/StatusBadge';

function ProgressBar({ value }: { value: number }) {
  return (
    <div style={{ width: '100%', height: '8px', backgroundColor: '#1e293b', borderRadius: '4px', overflow: 'hidden' }}>
      <div
        style={{
          width: `${Math.min(value, 100)}%`,
          height: '100%',
          backgroundColor: value === 100 ? '#34d399' : '#38bdf8',
          borderRadius: '4px',
          transition: 'width 0.3s ease',
        }}
      />
    </div>
  );
}

export default function BuildsPage() {
  const [builds, setBuilds] = useState<Build[]>([]);

  useEffect(() => {
    api.getBuilds().then(setBuilds).catch(() => setBuilds([]));
  }, []);

  return (
    <div>
      <h2 style={{ marginTop: 0, color: '#f8fafc', marginBottom: '16px' }}>Builds</h2>
      <JobTable
        rows={builds as unknown as Record<string, unknown>[]}
        columns={[
          { key: 'name', label: 'Name', sortable: true },
          {
            key: 'status',
            label: 'Status',
            sortable: true,
            render: (v) => <StatusBadge status={v as Build['status']} />,
          },
          {
            key: 'progress',
            label: 'Progress',
            sortable: true,
            render: (v) => <ProgressBar value={Number(v)} />,
          },
          { key: 'startedAt', label: 'Started', sortable: true },
          { key: 'durationSec', label: 'Duration (s)', sortable: true },
        ]}
      />
    </div>
  );
}
