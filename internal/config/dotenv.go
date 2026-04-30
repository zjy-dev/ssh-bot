package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadDotenvIfExists loads the first existing .env file from the candidate
// paths into the process environment. Existing env vars win and are never
// overwritten, so production-injected secrets still take precedence.
func LoadDotenvIfExists(paths ...string) (string, error) {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}

		info, err := os.Stat(clean)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat %q: %w", clean, err)
		}
		if info.IsDir() {
			continue
		}
		if err := loadDotenvFile(clean); err != nil {
			return "", err
		}
		return clean, nil
	}
	return "", nil
}

// DotenvCandidatePaths returns the common local-dev .env locations for a bot
// config path: current working directory, next to the config file, and the
// config directory's parent (repo-root when config lives in configs/).
func DotenvCandidatePaths(configPath string) []string {
	paths := []string{".env"}
	if configPath == "" {
		return paths
	}

	configDir := filepath.Dir(configPath)
	paths = append(paths,
		filepath.Join(configDir, ".env"),
		filepath.Join(configDir, "..", ".env"),
	)
	return paths
}

func loadDotenvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("parse %q:%d: missing '='", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("parse %q:%d: empty key", path, lineNo)
		}
		if strings.ContainsAny(key, " \t") {
			return fmt.Errorf("parse %q:%d: invalid key %q", path, lineNo, key)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}

		value, err := parseDotenvValue(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("parse %q:%d: %w", path, lineNo, err)
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %q from %q: %w", key, path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %q: %w", path, err)
	}
	return nil
}

func parseDotenvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	switch raw[0] {
	case '\'':
		end := strings.Index(raw[1:], "'")
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		end++
		value := raw[1:end]
		if err := validateDotenvTail(raw[end+1:]); err != nil {
			return "", err
		}
		return value, nil
	case '"':
		end := findClosingDoubleQuote(raw)
		if end < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		value, err := strconv.Unquote(raw[:end+1])
		if err != nil {
			return "", err
		}
		if err := validateDotenvTail(raw[end+1:]); err != nil {
			return "", err
		}
		return value, nil
	default:
		if idx := strings.Index(raw, " #"); idx >= 0 {
			raw = raw[:idx]
		}
		return strings.TrimSpace(raw), nil
	}
}

func validateDotenvTail(tail string) error {
	tail = strings.TrimSpace(tail)
	if tail == "" || strings.HasPrefix(tail, "#") {
		return nil
	}
	return fmt.Errorf("unexpected trailing content %q", tail)
}

func findClosingDoubleQuote(raw string) int {
	for i := 1; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] == '"' {
			return i
		}
	}
	return -1
}
