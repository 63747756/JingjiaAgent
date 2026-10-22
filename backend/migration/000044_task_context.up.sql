ALTER TABLE runtime_task_intents
  ADD COLUMN context_reset_id uuid,
  ADD COLUMN context_reset_turn bigint NOT NULL DEFAULT 0,
  ADD COLUMN context_reset_at timestamptz,
  ADD COLUMN context_usage jsonb;
