import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { ClusterSummary } from '../api/types';
import StatusBadge from '../components/StatusBadge';

function StatCard({ label, value, color }: { label: string; value: React.ReactNode; color?: string }) {
  return (
    <div style={{ backgroundColor: '#0f172a', border: '1px solid #1e293b', borderRadius: '8px', padding: '16px' }}>
      <div style={{ fontSize: '0.8rem', color: '#94a3b8', marginBottom: '6px' }}>{label}</div>
      <div style={{ fontSize: '1.5rem', fontWeight: 700, color: color ?? '#f8fafc' }}>{value}</div>
    </div>
  );
}

export default function ClusterOverview() {
  const [summary, setSummary] = useState<ClusterSummary | null>(null);

  useEffect(() => {
    api.getSummary().then(setSummary).catch(() => setSummary(null));
  }, []);

  if (!summary) {
    return <div style={{ color: '#94a3b8' }}>Loading cluster overview...</div>;
  }

  return (
    <div>
      <h2 style={{ marginTop: 0, color: '#f8fafc' }}>Cluster Overview</h2>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: '16px', marginBottom: '24px' }}>
        <StatCard label="Status" value={<StatusBadge status={summary.status} />} />
        <StatCard label="Nodes" value={`${summary.healthyNodes} / ${summary.nodeCount}`} color="#38bdf8" />
        <StatCard label="Active Jobs" value={summary.activeJobs} color="#34d399" />
        <StatCard label="Total Jobs" value={summary.totalJobs} color="#a78bfa" />
        <StatCard label="Active Sessions" value={summary.activeSessions} color="#fbbf24" />
        <StatCard label="Builds Today" value={summary.buildsToday} color="#f472b6" />
        <StatCard label="Failed Builds" value={summary.failedBuilds} color="#f87171" />
      </div>
    </div>
  );
}
