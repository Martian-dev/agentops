-- Create agents table
CREATE TABLE agents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  description TEXT,
  system_prompt_id TEXT,
  tool_ids UUID[],
  model_config JSONB,
  active_version INT DEFAULT 1,
  created_at TIMESTAMPTZ DEFAULT now()
);

-- Create indexes for common queries
CREATE INDEX idx_agents_name ON agents(name);
CREATE INDEX idx_agents_active_version ON agents(active_version);
