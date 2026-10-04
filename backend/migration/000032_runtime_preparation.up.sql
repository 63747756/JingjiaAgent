-- Task detail polling reads preparation without scanning user-turn commands.
CREATE INDEX runtime_commands_preparation
    ON runtime_commands (environment_id, created_at DESC, id DESC)
    WHERE operation = 'prepare';
