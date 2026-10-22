DROP TRIGGER IF EXISTS runtime_host_remove_guard ON hosts;
DROP FUNCTION IF EXISTS guard_runtime_host_removal();
DROP TABLE IF EXISTS runtime_node_configs;
DROP FUNCTION IF EXISTS notify_runtime_node_config();
