package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProjectName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"valid simple", "myproject", false, ""},
		{"valid with dash", "my-project", false, ""},
		{"valid with underscore", "my_project", false, ""},
		{"valid alphanumeric", "project123", false, ""},
		{"valid mixed", "My-Project_123", false, ""},
		{"valid max length", "a123456789012345678901234567890123456789012345678901234567890123", false, ""}, // 64 chars

		// Invalid cases
		{"empty string", "", true, "project name cannot be empty"},
		{"too long", "a1234567890123456789012345678901234567890123456789012345678901234", true, "invalid characters"}, // 65 chars
		{"with spaces", "my project", true, "invalid characters"},
		{"with slash", "my/project", true, "invalid characters"},
		{"with backslash", "my\\project", true, "invalid characters"},
		{"with dots", "my.project", true, "invalid characters"},
		{"path traversal", "../etc", true, "invalid characters"},
		{"command injection attempt", "project;ls", true, "invalid characters"},
		{"with quotes", "project'name", true, "invalid characters"},
		{"with backticks", "project`ls`", true, "invalid characters"},
		{"with pipe", "project|cmd", true, "invalid characters"},
		{"with ampersand", "project&cmd", true, "invalid characters"},
		{"with redirect", "project>file", true, "invalid characters"},
		{"with parentheses", "project(cmd)", true, "invalid characters"},
		{"with dollar sign", "project$VAR", true, "invalid characters"},
		{"unicode chars", "项目", true, "invalid characters"},
		{"control chars", "project\n", true, "invalid characters"},
		{"tab character", "project\t", true, "invalid characters"},
		{"null byte", "project\x00", true, "invalid characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProjectName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateProjectName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() == "" {
				t.Errorf("validateProjectName(%q) error message is empty, want substring %q", tt.input, tt.errMsg)
			}
		})
	}
}

func TestValidateFilePath(t *testing.T) {
	// Create a temporary file for testing
	tmpDir := t.TempDir()
	validFile := filepath.Join(tmpDir, "compose.yml")
	if err := os.WriteFile(validFile, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a directory for testing
	dirPath := filepath.Join(tmpDir, "mydir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"valid absolute path", validFile, false, ""},

		// Invalid cases
		{"empty string", "", true, "cannot be empty"},
		{"relative path", "compose.yml", true, "must be absolute"},
		{"relative with dots", "./compose.yml", true, "must be absolute"},
		{"path traversal", "/etc/../etc/passwd", true, "path traversal"},
		{"path traversal hidden", "/app/config/../../../etc/passwd", true, "path traversal"},
		{"non-existent file", "/does/not/exist/compose.yml", true, "does not exist"},
		{"directory not file", dirPath, true, "not a regular file"},
		{"with null byte", "/etc/compose\x00.yml", true, ""},
		{"with newline", "/etc/compose\n.yml", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFilePath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFilePath(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() == "" {
				t.Errorf("validateFilePath(%q) error message is empty, want substring %q", tt.input, tt.errMsg)
			}
		})
	}
}

func TestValidateEnvFilePath(t *testing.T) {
	// Create a temporary file for testing
	tmpDir := t.TempDir()
	validFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(validFile, []byte("KEY=value\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"empty string (optional)", "", false, ""},
		{"valid absolute path", validFile, false, ""},

		// Invalid cases
		{"relative path", ".env", true, "must be absolute"},
		{"path traversal", "/etc/../etc/passwd", true, "path traversal"},
		{"non-existent file", "/does/not/exist/.env", true, "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEnvFilePath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateEnvFilePath(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() == "" {
				t.Errorf("validateEnvFilePath(%q) error message is empty, want substring %q", tt.input, tt.errMsg)
			}
		})
	}
}

func TestValidateComposeFiles(t *testing.T) {
	// Create temporary files for testing
	tmpDir := t.TempDir()
	file1 := filepath.Join(tmpDir, "compose1.yml")
	file2 := filepath.Join(tmpDir, "compose2.yml")
	if err := os.WriteFile(file1, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		input   []string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"single valid file", []string{file1}, false, ""},
		{"multiple valid files", []string{file1, file2}, false, ""},

		// Invalid cases
		{"empty slice", []string{}, true, "at least one compose file is required"},
		{"one invalid file", []string{file1, "/does/not/exist.yml"}, true, "does not exist"},
		{"relative path", []string{"compose.yml"}, true, "must be absolute"},
		{"path traversal", []string{"/etc/../etc/passwd"}, true, "path traversal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateComposeFiles(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateComposeFiles(%v) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() == "" {
				t.Errorf("validateComposeFiles(%v) error message is empty, want substring %q", tt.input, tt.errMsg)
			}
		})
	}
}