package tools

import "os"

// ReadFile reads the file at path and returns its contents as a string.
func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile writes content to the file at path, creating it if necessary.
func WriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
