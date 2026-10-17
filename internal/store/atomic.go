package store

import (
	"encoding/json"
	"fmt"

	"bifrost/internal/atomicfile"
)

func atomicJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	b = append(b, '\n')
	return atomicfile.Write(path, b)
}
