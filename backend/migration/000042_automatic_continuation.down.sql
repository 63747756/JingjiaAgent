DROP INDEX IF EXISTS runtime_commands_waiting;
ALTER TABLE runtime_commands DROP COLUMN IF EXISTS recovery_checkpoint_id;
ALTER TABLE runtime_commands DROP COLUMN IF EXISTS preparation_checkpoint_id;
ALTER TABLE runtime_commands DROP COLUMN IF EXISTS preparation_started_at;
ALTER TABLE runtime_commands DROP COLUMN IF EXISTS preparation_error;
ALTER TABLE runtime_commands DROP COLUMN IF EXISTS preparation_phase;
