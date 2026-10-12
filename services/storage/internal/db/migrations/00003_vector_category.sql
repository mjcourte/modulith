-- +goose Up

-- SVGs are now distinct dam.CategoryVector; before they were CategoryImage.
UPDATE storage.assets
SET category = 'vector'
WHERE category = 'image' AND lower(content_type) LIKE 'image/svg+xml%';

-- +goose Down
UPDATE storage.assets
SET category = 'image' WHERE category = 'vector';
