-- +goose Up
ALTER TABLE guest_registrations ADD COLUMN renewal_predecessor_serial TEXT;
ALTER TABLE guest_registrations ADD COLUMN renewal_certificate_der TEXT;

-- +goose Down
ALTER TABLE guest_registrations DROP COLUMN renewal_certificate_der;
ALTER TABLE guest_registrations DROP COLUMN renewal_predecessor_serial;
