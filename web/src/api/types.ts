export type HealthStatus = 'Healthy' | 'Degraded' | 'Critical' | 'Unknown';

export interface Node {
  id: string;
  name: string;
  address: string;
  status: HealthStatus;
  cpuPercent: number;
  memoryPercent: number;
  gpuPercent: number;
  gpuCount: number;
  labels: Record<string, string>;
}

export interface Job {
  id: string;
  name: string;
  status: HealthStatus | 'Running' | 'Pending' | 'Completed' | 'Failed';
  type: string;
  createdAt: string;
  nodeId?: string;
}

export interface Session {
  id: string;
  user: string;
  client: string;
  startedAt: string;
  lastActiveAt: string;
  status: HealthStatus | 'Active' | 'Idle' | 'Closed';
}

export interface Build {
  id: string;
  name: string;
  status: HealthStatus | 'Building' | 'Success' | 'Failed' | 'Queued';
  progress: number;
  startedAt: string;
  durationSec: number;
}

export interface HealthCheck {
  name: string;
  status: HealthStatus;
  message: string;
  lastChecked: string;
}

export interface SecurityPolicy {
  id: string;
  name: string;
  type: string;
  status: HealthStatus | 'Enforced' | 'Audit' | 'Disabled';
  updatedAt: string;
}

export interface ClusterSummary {
  status: HealthStatus;
  nodeCount: number;
  healthyNodes: number;
  totalJobs: number;
  activeJobs: number;
  activeSessions: number;
  buildsToday: number;
  failedBuilds: number;
}
