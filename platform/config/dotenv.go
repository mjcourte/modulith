package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// readDotEnv loads path if it exists. A missing file returns no settings
// and no error.
func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vars, err := parseDotEnv(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return vars, nil
}

// parseDotEnv reads KEY=VALUE lines. Blank lines and lines starting with #
// are skipped, and a value may be wrapped in single or double quotes. There
// is no variable expansion or escaping; keep values simple.
func parseDotEnv(r io.Reader) (map[string]string, error) {
	vars := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		vars[key] = value
	}
	return vars, scanner.Err()
}
