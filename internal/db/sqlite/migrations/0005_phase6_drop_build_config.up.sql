ALTER TABLE previews ADD COLUMN repo_clone_url TEXT NOT NULL DEFAULT '';
ALTER TABLE previews ADD COLUMN preview_urls TEXT NOT NULL DEFAULT '';
ALTER TABLE previews DROP COLUMN public_url;
ALTER TABLE agents DROP COLUMN run_commands;
ALTER TABLE agents DROP COLUMN container_port;
