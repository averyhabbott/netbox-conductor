-- Add maintenance_mode flag to nodes.
-- When true the agent will not auto-start NetBox on Patroni promotion and
-- the node is excluded from auto-failover target selection in the UI.
ALTER TABLE nodes ADD COLUMN maintenance_mode BOOLEAN NOT NULL DEFAULT FALSE;
