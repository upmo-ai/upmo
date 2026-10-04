-- +goose Up

ALTER TABLE knowledge.sources
  DROP CONSTRAINT sources_folder_id_fkey;

ALTER TABLE knowledge.sources
  ADD CONSTRAINT sources_folder_id_fkey
  FOREIGN KEY (folder_id) REFERENCES knowledge.folders (id) ON DELETE CASCADE;

-- +goose Down

ALTER TABLE knowledge.sources
  DROP CONSTRAINT sources_folder_id_fkey;

ALTER TABLE knowledge.sources
  ADD CONSTRAINT sources_folder_id_fkey
  FOREIGN KEY (folder_id) REFERENCES knowledge.folders (id);
