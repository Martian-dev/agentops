-- Add checkpoint and execution metadata columns for run resumption
ALTER TABLE agent_runs ADD COLUMN checkpoint_data JSONB;
ALTER TABLE agent_runs ADD COLUMN execution_metadata JSONB;
