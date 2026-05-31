import type { Node } from '../api/types';
import StatusBadge from './StatusBadge';
import ResourceChart from './ResourceChart';

export default function NodeCard({ node }: { node: Node }) {
  return (
    <div
      style={{
        backgroundColor: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '8px',
        padding: '16px',
        display: 'flex',
        flexDirection: 'column',
        gap: '12px',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h3 style={{ margin: 0, fontSize: '1rem', color: '#f1f5f9' }}>{node.name}</h3>
        <StatusBadge status={node.status} />
      </div>
      <div style={{ fontSize: '0.8rem', color: '#64748b' }}>
        {node.address} · {node.gpuCount > 0 ? `${node.gpuCount} GPU` : 'No GPU'}
      </div>
      <ResourceChart cpu={node.cpuPercent} memory={node.memoryPercent} gpu={node.gpuPercent} />
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
        {Object.entries(node.labels).map(([k, v]) => (
          <span
            key={k}
            style={{
              fontSize: '0.7rem',
              padding: '2px 6px',
              borderRadius: '4px',
              backgroundColor: '#1e293b',
              color: '#94a3b8',
            }}
          >
            {k}={v}
          </span>
        ))}
      </div>
    </div>
  );
}
