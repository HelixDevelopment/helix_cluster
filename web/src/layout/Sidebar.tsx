import { NavLink } from 'react-router-dom';

const links = [
  { to: '/', label: 'Cluster' },
  { to: '/nodes', label: 'Nodes' },
  { to: '/jobs', label: 'Jobs' },
  { to: '/sessions', label: 'Sessions' },
  { to: '/builds', label: 'Builds' },
  { to: '/health', label: 'Health' },
  { to: '/security', label: 'Security' },
];

export default function Sidebar() {
  return (
    <aside
      style={{
        width: '200px',
        backgroundColor: '#020617',
        borderRight: '1px solid #1e293b',
        display: 'flex',
        flexDirection: 'column',
        padding: '16px 0',
      }}
    >
      <nav style={{ display: 'flex', flexDirection: 'column', gap: '4px', padding: '0 12px' }}>
        {links.map((link) => (
          <NavLink
            key={link.to}
            to={link.to}
            style={({ isActive }) => ({
              display: 'block',
              padding: '8px 12px',
              borderRadius: '6px',
              textDecoration: 'none',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: isActive ? '#f8fafc' : '#94a3b8',
              backgroundColor: isActive ? '#1e293b' : 'transparent',
            })}
          >
            {link.label}
          </NavLink>
        ))}
      </nav>
    </aside>
  );
}
