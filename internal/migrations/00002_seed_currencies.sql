-- +goose Up

-- Reference data, not application seeding logic: versioned, applied once, and
-- adding a currency later is the same gesture as this one -- a new migration.
--
-- Only the currencies actually in use. A full ISO 4217 catalogue would be rows
-- nobody reads. created_at and updated_at come from the column defaults.
INSERT INTO currencies (code, name, symbol, minor_unit) VALUES
    ('USD', 'US Dollar',        '$',   2),
    ('EUR', 'Euro',             '€',   2),
    ('VES', 'Bolívar Soberano', 'Bs.', 2);

-- +goose Down

DELETE FROM currencies WHERE code IN ('USD', 'EUR', 'VES');
