package auth

import (
	"testing"
)

func TestValidateAuthInput(t *testing.T) {
	tests := []struct {
		name        string
		username    string
		password    string
		expectError bool
	}{
		{"Valid User", "john_doe", "password123", false},
		{"Valid User with Dot and Hyphen", "john.doe-dev", "secret123", false},
		{"Too Short Username", "a", "password123", true},
		{"Too Long Username", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "password123", true},
		{"Username with Space", "john doe", "password123", true},
		{"Username with Shell Characters", "john;rm -rf /", "password123", true},
		{"Username with Slash", "john/doe", "password123", true},
		{"Too Short Password", "john", "123", true},
		{"Valid Min Password", "john", "1234", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAuthInput(tt.username, tt.password)
			if (err != nil) != tt.expectError {
				t.Errorf("validateAuthInput(%q, %q) error = %v, expectError = %v", tt.username, tt.password, err, tt.expectError)
			}
		})
	}
}
