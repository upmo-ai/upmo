-- +goose Up
CREATE TABLE two_mark (id int PRIMARY KEY);

-- +goose Down
DROP TABLE two_mark;
