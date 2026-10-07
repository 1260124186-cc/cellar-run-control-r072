package archive

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func newGroupID() (string, error) {
	buffer := make([]byte, 10)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate archive group identifier: %w", err)
	}
	return "grp_" + hex.EncodeToString(buffer), nil
}

func newEventID() (string, error) {
	buffer := make([]byte, 10)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate archive event identifier: %w", err)
	}
	return "evt_" + hex.EncodeToString(buffer), nil
}
