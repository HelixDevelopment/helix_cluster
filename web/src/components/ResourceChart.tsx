export default function ResourceChart({
  cpu,
  memory,
  gpu,
}: {
  cpu: number;
  memory: number;
  gpu: number;
}) {
  const bar = (label: string, value: number, color: string) => (
    <div style={{ marginBottom: '8px' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.75rem', color: '#94a3b8', marginBottom: '2px' }}>
        <span>{label}</span>
        <span>{value}%</span>
      </div>
      <div style={{ width: '100%', height: '8px', backgroundColor: '#1e293b', borderRadius: '4px', overflow: 'hidden' }}>
        <div
          style={{
            width: `${Math.min(value, 100)}%`,
            height: '100%',
            backgroundColor: color,
            borderRadius: '4px',
            transition: 'width 0.3s ease',
          }}
        />
      </div>
    </div>
  );

  return (
    <div>
      {bar('CPU', cpu, '#38bdf8')}
      {bar('Memory', memory, '#a78bfa')}
      {bar('GPU', gpu, '#34d399')}
    </div>
  );
}
