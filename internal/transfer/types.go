package transfer

import (
	"encoding/json"
	"errors"
)

// ErrUnsupportedProvider indicates the provider name is not recognized.
var ErrUnsupportedProvider = errors.New("unsupported provider")

// AccountItem represents a single exported account with its alias and native token snapshot.
type AccountItem struct {
	Alias string          `json:"alias,omitempty"`
	Token json.RawMessage `json:"token"`
}

// Bundle represents an export bundle of accounts grouped by provider name.
type Bundle map[string][]AccountItem
