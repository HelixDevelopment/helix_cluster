import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { Job } from '../api/types';
import JobTable from '../components/JobTable';
import StatusBadge from '../components/StatusBadge';

export default function JobsPage() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [filter, setFilter] = useState<string>('All');

  useEffect(() => {
    api.getJobs().then(setJobs).catch(() => setJobs([]));
  }, []);

  const statuses = ['All', ...Array.from(new Set(jobs.map((j) => j.status)))];
  const filtered = filter === 'All' ? jobs : jobs.filter((j) => j.status === filter);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
        <h2 style={{ margin: 0, color: '#f8fafc' }}>Jobs</h2>
        <select
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          style={{
            backgroundColor: '#0f172a',
            color: '#e2e8f0',
            border: '1px solid #334155',
            borderRadius: '6px',
            padding: '6px 10px',
            fontSize: '0.875rem',
          }}
        >
          {statuses.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </div>
      <JobTable
        rows={filtered as unknown as Record<string, unknown>[]}
        columns={[
          { key: 'name', label: 'Name', sortable: true },
          { key: 'type', label: 'Type', sortable: true },
          {
            key: 'status',
            label: 'Status',
            sortable: true,
            render: (v) => <StatusBadge status={v as Job['status']} />,
          },
          { key: 'nodeId', label: 'Node', sortable: true },
          { key: 'createdAt', label: 'Created', sortable: true },
        ]}
      />
    </div>
  );
}
