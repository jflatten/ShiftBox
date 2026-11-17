-- +goose Up
CREATE TABLE ec2_launch_enrollment (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id),
    launch_reference TEXT NOT NULL UNIQUE CHECK (length(launch_reference) = 64),
    public_data TEXT,
    guest_key_proof TEXT
);
CREATE TABLE ec2_launch_challenges (
    nonce TEXT PRIMARY KEY CHECK (length(nonce) = 64),
    operation_id TEXT NOT NULL REFERENCES ec2_launch_enrollment(operation_id) ON DELETE CASCADE,
    guest_key_proof TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    UNIQUE (operation_id, guest_key_proof)
);

-- +goose Down
DROP TABLE ec2_launch_challenges;
DROP TABLE ec2_launch_enrollment;
