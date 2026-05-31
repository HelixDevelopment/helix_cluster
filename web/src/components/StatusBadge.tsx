import type { HealthStatus } from '../api/types';

type BadgeStatus = HealthStatus | 'Running' | 'Pending' | 'Completed' | 'Failed' | 'Building' | 'Success' | 'Queued' | 'Active' | 'Idle' | 'Closed' | 'Enforced' | 'Audit' | 'Disabled';

const statusColors: Record<string, { bg: string; text: string }> = {
  Healthy: { bg: '#14532d', text: '#86efac' },
  Active: { bg: '#14532d', text: '#86efac' },
  Enforced: { bg: '#14532d', text: '#86efac' },
  Success: { bg: '#14532d', text: '#86efac' },
  Completed: { bg: '#14532d', text: '#86efac' },
  Degraded: { bg: '#713f12', text: '#fde047' },
  Pending: { bg: '#713f12', text: '#fde047' },
  Queued: { bg: '#713f12', text: '#fde047' },
  Idle: { bg: '#713f12', text: '#fde047' },
  Audit: { bg: '#713f12', text: '#fde047' },
  Critical: { bg: '#450a0a', text: '#fca5a5' },
  Failed: { bg: '#450a0a', text: '#fca5a5' },
  Closed: { bg: '#450a0a', text: '#fca5a5' },
  Disabled: { bg: '#450a0a', text: '#fca5a5' },
  Unknown: { bg: '#1e293b', text: '#94a3b8' },
  Running: { bg: '#0c4a6e', text: '#7dd3fc' },
  Building: { bg: '#0c4a6e', text: '#7dd3fc' },
};

export default function StatusBadge({ status }: { status: BadgeStatus }) {
  const colors = statusColors[status] || statusColors['Unknown'];
  return (
    <span
      style={{
        display: 'inline-block',
        padding: '2px 8px',
        borderRadius: '4px',
        fontSize: '0.75rem',
        fontWeight: 600,
        textTransform: 'uppercase',
        letterSpacing: '0.05em',
        backgroundColor: colors.bg,
        color: colors.text,
      }}
    >
      {status}
    </span>
  );
}
