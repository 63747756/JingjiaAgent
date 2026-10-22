-- A waiting command is an accepted input, but has not reached the runtime.
ALTER TABLE runtime_commands ADD COLUMN preparation_phase text NOT NULL DEFAULT '';
ALTER TABLE runtime_commands ADD COLUMN preparation_error text NOT NULL DEFAULT '';
ALTER TABLE runtime_commands ADD COLUMN preparation_started_at timestamptz;
ALTER TABLE runtime_commands ADD COLUMN preparation_checkpoint_id uuid;
ALTER TABLE runtime_commands ADD COLUMN recovery_checkpoint_id uuid;
CREATE INDEX runtime_commands_waiting ON runtime_commands(available_at,created_at) WHERE state='waiting';
