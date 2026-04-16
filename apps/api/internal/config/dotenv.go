package config

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// LoadDotEnv reads a .env file and sets environment variables that are not
// already defined. This means system env vars and CLI flags always take
// precedence over the .env file.
//
// Supports:
//   - KEY=VALUE
//   - KEY="VALUE" and KEY='VALUE' (quoted values, quotes stripped)
//   - # comments and blank lines (skipped)
//   - No variable expansion (${VAR} is treated as literal)
func LoadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return // .env not found — silently skip
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first =
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Strip surrounding quotes
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		// Only set if not already defined (env vars take precedence)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
			count++
		}
	}

	if count > 0 {
		log.Printf("Loaded %d variables from %s", count, path)
	}
}
