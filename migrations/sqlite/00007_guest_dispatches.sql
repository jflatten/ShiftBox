-- +goose Up
ALTER TABLE guest_configurations RENAME TO guest_dispatches;

-- +goose Down
ALTER TABLE guest_dispatches RENAME TO guest_configurations;
