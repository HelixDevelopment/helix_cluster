-- seed-data.sql — development seed data for Helix Cluster

-- Create sample schema if not exists
CREATE SCHEMA IF NOT EXISTS helix;

-- Nodes table
CREATE TABLE IF NOT EXISTS helix.nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname TEXT NOT NULL,
    address INET NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('master', 'worker', 'edge')),
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Clusters table
CREATE TABLE IF NOT EXISTS helix.clusters (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    region TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'provisioning',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Seed nodes
INSERT INTO helix.nodes (hostname, address, role, status) VALUES
    ('node-alpha', '10.0.1.10', 'master', 'active'),
    ('node-beta',  '10.0.1.11', 'worker', 'active'),
    ('node-gamma', '10.0.1.12', 'worker', 'active'),
    ('node-delta', '10.0.1.13', 'edge',   'active')
ON CONFLICT DO NOTHING;

-- Seed clusters
INSERT INTO helix.clusters (name, region, status) VALUES
    ('helix-prod-us-east', 'us-east-1', 'active'),
    ('helix-prod-eu-west', 'eu-west-1', 'active'),
    ('helix-staging',      'us-west-2', 'provisioning')
ON CONFLICT DO NOTHING;
