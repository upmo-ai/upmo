-- +goose Up
CREATE TABLE one_mark (id int PRIMARY KEY);

-- +goose Down
DROP TABLE one_mark;
