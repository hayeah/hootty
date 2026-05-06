package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type stringListFlag []string

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

func parseEnvOverrides(envFiles, envSpecs []string, lookup func(string) (string, bool)) (map[string]string, error) {
	out := map[string]string{}
	for _, path := range envFiles {
		values, err := parseEnvFile(path)
		if err != nil {
			return nil, err
		}
		for name, value := range values {
			out[name] = value
		}
	}
	for _, spec := range envSpecs {
		name, value, hasValue := strings.Cut(spec, "=")
		if err := validateEnvName(name); err != nil {
			return nil, fmt.Errorf("--env %q: %w", spec, err)
		}
		if !hasValue {
			var ok bool
			value, ok = lookup(name)
			if !ok {
				return nil, fmt.Errorf("--env %s: environment variable is not set", name)
			}
		}
		out[name] = value
	}
	return out, nil
}

func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--env-file %s: %w", path, err)
	}
	defer f.Close()

	out := map[string]string{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("--env-file %s:%d: expected NAME=VALUE", path, lineNo)
		}
		if err := validateEnvName(name); err != nil {
			return nil, fmt.Errorf("--env-file %s:%d: %w", path, lineNo, err)
		}
		out[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("--env-file %s: %w", path, err)
	}
	return out, nil
}

func validateEnvName(name string) error {
	if name == "" {
		return fmt.Errorf("empty environment variable name")
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return fmt.Errorf("invalid environment variable name %q", name)
		}
	}
	return nil
}
