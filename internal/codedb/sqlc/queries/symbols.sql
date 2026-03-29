-- name: UpdateSymbolParent :exec
UPDATE symbols SET parent_id = ? WHERE id = ?;
