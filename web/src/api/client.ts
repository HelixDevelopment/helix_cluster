import type {
  Node,
  Job,
  Session,
  Build,
  HealthCheck,
  SecurityPolicy,
  ClusterSummary,
} from './types';

const BASE_URL = 'http://localhost:8080/v1';

async function fetchJson<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json() as Promise<T>;
}

const USE_MOCK = true;

const mockNodes: Node[] = [
  {
    id: 'n1',
    name: 'alpha',
    address: '10.0.1.11',
    status: 'Healthy',
    cpuPercent: 34,
    memoryPercent: 62,
    gpuPercent: 12,
    gpuCount: 4,
    labels: { zone: 'us-east-1a', gpu: 'nvidia' },
  },
  {
    id: 'n2',
    name: 'beta',
    address: '10.0.1.12',
    status: 'Healthy',
    cpuPercent: 56,
    memoryPercent: 48,
    gpuPercent: 0,
    gpuCount: 0,
    labels: { zone: 'us-east-1b' },
  },
  {
    id: 'n3',
    name: 'gamma',
    address: '10.0.1.13',
    status: 'Degraded',
    cpuPercent: 88,
    memoryPercent: 91,
    gpuPercent: 45,
    gpuCount: 2,
    labels: { zone: 'us-east-1c', gpu: 'nvidia' },
  },
  {
    id: 'n4',
    name: 'delta',
    address: '10.0.1.14',
    status: 'Critical',
    cpuPercent: 97,
    memoryPercent: 95,
    gpuPercent: 0,
    gpuCount: 0,
    labels: { zone: 'us-east-1d' },
  },
];

const mockJobs: Job[] = [
  { id: 'j1', name: 'train-resnet', status: 'Running', type: 'ml', createdAt: '2026-05-30T08:00:00Z', nodeId: 'n1' },
  { id: 'j2', name: 'data-pipeline', status: 'Running', type: 'batch', createdAt: '2026-05-30T09:00:00Z', nodeId: 'n2' },
  { id: 'j3', name: 'index-refresh', status: 'Pending', type: 'batch', createdAt: '2026-05-30T10:00:00Z' },
  { id: 'j4', name: 'model-eval', status: 'Completed', type: 'ml', createdAt: '2026-05-29T12:00:00Z', nodeId: 'n1' },
  { id: 'j5', name: 'backup-job', status: 'Failed', type: 'maintenance', createdAt: '2026-05-29T22:00:00Z', nodeId: 'n3' },
];

const mockSessions: Session[] = [
  { id: 's1', user: 'alice', client: 'cli', startedAt: '2026-05-30T06:00:00Z', lastActiveAt: '2026-05-30T20:00:00Z', status: 'Active' },
  { id: 's2', user: 'bob', client: 'web', startedAt: '2026-05-30T14:00:00Z', lastActiveAt: '2026-05-30T19:30:00Z', status: 'Active' },
  { id: 's3', user: 'carol', client: 'sdk', startedAt: '2026-05-29T10:00:00Z', lastActiveAt: '2026-05-30T18:00:00Z', status: 'Idle' },
];

const mockBuilds: Build[] = [
  { id: 'b1', name: 'helix-node', status: 'Building', progress: 67, startedAt: '2026-05-30T19:00:00Z', durationSec: 4200 },
  { id: 'b2', name: 'helix-gateway', status: 'Success', progress: 100, startedAt: '2026-05-30T16:00:00Z', durationSec: 1800 },
  { id: 'b3', name: 'helix-scheduler', status: 'Failed', progress: 45, startedAt: '2026-05-30T15:00:00Z', durationSec: 600 },
  { id: 'b4', name: 'helix-web', status: 'Queued', progress: 0, startedAt: '2026-05-30T20:00:00Z', durationSec: 0 },
];

const mockHealth: HealthCheck[] = [
  { name: 'etcd', status: 'Healthy', message: 'cluster quorum ok', lastChecked: '2026-05-30T20:40:00Z' },
  { name: 'scheduler', status: 'Healthy', message: 'leader elected', lastChecked: '2026-05-30T20:40:00Z' },
  { name: 'gateway', status: 'Healthy', message: 'all endpoints reachable', lastChecked: '2026-05-30T20:40:00Z' },
  { name: 'gpu-node-pool', status: 'Degraded', message: '1 node reporting high memory', lastChecked: '2026-05-30T20:40:00Z' },
  { name: 'storage-backend', status: 'Critical', message: 'connection timeout', lastChecked: '2026-05-30T20:38:00Z' },
];

const mockPolicies: SecurityPolicy[] = [
  { id: 'p1', name: 'tls-min-version', type: 'transport', status: 'Enforced', updatedAt: '2026-05-01T00:00:00Z' },
  { id: 'p2', name: 'rbac-default-deny', type: 'access', status: 'Enforced', updatedAt: '2026-05-10T00:00:00Z' },
  { id: 'p3', name: 'audit-logging', type: 'audit', status: 'Audit', updatedAt: '2026-05-20T00:00:00Z' },
];

const mockSummary: ClusterSummary = {
  status: 'Degraded',
  nodeCount: 4,
  healthyNodes: 2,
  totalJobs: 5,
  activeJobs: 2,
  activeSessions: 2,
  buildsToday: 4,
  failedBuilds: 1,
};

export const api = {
  getNodes: (): Promise<Node[]> => (USE_MOCK ? Promise.resolve(mockNodes) : fetchJson<Node[]>('/nodes')),
  getJobs: (): Promise<Job[]> => (USE_MOCK ? Promise.resolve(mockJobs) : fetchJson<Job[]>('/jobs')),
  getSessions: (): Promise<Session[]> => (USE_MOCK ? Promise.resolve(mockSessions) : fetchJson<Session[]>('/sessions')),
  getBuilds: (): Promise<Build[]> => (USE_MOCK ? Promise.resolve(mockBuilds) : fetchJson<Build[]>('/builds')),
  getHealth: (): Promise<HealthCheck[]> => (USE_MOCK ? Promise.resolve(mockHealth) : fetchJson<HealthCheck[]>('/health')),
  getPolicies: (): Promise<SecurityPolicy[]> => (USE_MOCK ? Promise.resolve(mockPolicies) : fetchJson<SecurityPolicy[]>('/security/policies')),
  getSummary: (): Promise<ClusterSummary> => (USE_MOCK ? Promise.resolve(mockSummary) : fetchJson<ClusterSummary>('/health/summary')),
};
