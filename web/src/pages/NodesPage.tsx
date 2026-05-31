import { useEffect, useState } from 'react';
import { api } from '../api/client';
import type { Node } from '../api/types';
import NodeCard from '../components/NodeCard';

export default function NodesPage() {
  const [nodes, setNodes] = useState<Node[]>([]);

  useEffect(() => {
    api.getNodes().then(setNodes).catch(() => setNodes([]));
  }, []);

  return (
    <div>
      <h2 style={{ marginTop: 0, color: '#f8fafc' }}>Nodes</h2>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '16px' }}>
        {nodes.map((node) => (
          <NodeCard key={node.id} node={node} />
        ))}
      </div>
    </div>
  );
}
