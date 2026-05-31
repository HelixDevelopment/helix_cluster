import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { ClusterSummary } from '../api/types';
import StatusBadge from '../components/StatusBadge';

export default function Header() {
  const [summary, setSummary] = useState<ClusterSummary | null>(null);

  useEffect(() => {
    api.getSummary().then(setSummary).catch(() => setSummary(null));
  }, []);

  return (
    <header
      style={{
        height: '56px',
        backgroundColor: '#0f172a',
        borderBottom: '1px solid #1e293b',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '0 24px',
        position: 'sticky',
        top: 0,
        zIndex: 10,
      }}
    >
      <div style={{ fontWeight: 700, fontSize: '1.1rem', color: '#f8fafc' }}>Helix Cluster OS</div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
        <span style={{ fontSize: '0.8rem', color: '#94a3b8' }}>Cluster Status</span>
        {summary ? (
          <StatusBadge status={summary.status} />
        ) : (
          <StatusBadge status="Unknown" />
        )}
      </div>
    </header>
  );
}
