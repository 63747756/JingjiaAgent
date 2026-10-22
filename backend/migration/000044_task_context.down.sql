ALTER TABLE runtime_task_intents
  DROP COLUMN context_usage,
  DROP COLUMN context_reset_at,
  DROP COLUMN context_reset_turn,
  DROP COLUMN context_reset_id;
