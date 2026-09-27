package capturerun

import (
	"encoding/hex"
	"errors"
)

// GitSnapshot is display-only launcher evidence, frozen before the child starts.
// RepositoryKey identifies a local clone; it is never a URL or filesystem path.
type GitSnapshot struct {
	RepositoryKey  string `json:"repositoryKey"`
	RepositoryName string `json:"repositoryName"`
	Branch         string `json:"branch"`
	Detached       bool   `json:"detached"`
}

func (value GitSnapshot) Validate() error {
	key, err := hex.DecodeString(value.RepositoryKey)
	if err != nil || len(key) != 32 || hex.EncodeToString(key) != value.RepositoryKey ||
		validateText("repository name", value.RepositoryName, 120) != nil ||
		(value.Detached && value.Branch != "") ||
		(!value.Detached && validateText("branch", value.Branch, 256) != nil) {
		return errors.New("invalid launch Git snapshot")
	}
	return nil
}
