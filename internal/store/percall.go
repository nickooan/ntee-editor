package store

import (
	"encoding/json"
	"sort"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// decodePrefix reads every record under prefix in key order and decodes
// each into a T, skipping any that does not decode: the scan-get-decode
// every list in this package does.
func decodePrefix[T any](db *nteedb.DB, prefix string) ([]T, error) {
	keys, err := db.PrefixScan(prefix)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	sort.Strings(keys)
	values, found, err := db.GetMany(keys)
	if err != nil {
		return nil, err
	}
	decoded := make([]T, 0, len(keys))
	for index := range keys {
		var value T
		if found[index] && json.Unmarshal(values[index], &value) == nil {
			decoded = append(decoded, value)
		}
	}
	return decoded, nil
}
