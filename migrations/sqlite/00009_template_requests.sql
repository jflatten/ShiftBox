-- +goose Up
CREATE TABLE template_requests (
 actor_id TEXT NOT NULL REFERENCES users(id),
 request_key TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 response_json TEXT NOT NULL,
 PRIMARY KEY (actor_id, request_key)
);

-- +goose Down
DROP TABLE template_requests;
