package capturerun

import (
	"encoding/hex"
	"errors"
)

// GitSnapshot is display-only launcher evidence, frozen before the child starts.
// RepositoryKey fingerprints a sanitized origin or a machine-local clone. It
// never contains a URL, credential, or filesystem path and grants no authority.
type GitSnapshot struct {
	RepositorySource string `json:"repositorySource"`
	RepositoryKey    string `json:"repositoryKey"`
	RepositoryName   string `json:"repositoryName"`
	Branch           string `json:"branch"`
	Detached         bool   `json:"detached"`
}

func (value GitSnapshot) Validate() error {
	key, err := hex.DecodeString(value.RepositoryKey)
	if (value.RepositorySource != "remote" && value.RepositorySource != "local") ||
		err != nil || len(key) != 32 || hex.EncodeToString(key) != value.RepositoryKey ||
		validateText("repository name", value.RepositoryName, 256) != nil ||
		(value.Detached && value.Branch != "") ||
		(!value.Detached && validateText("branch", value.Branch, 256) != nil) {
		return errors.New("invalid launch Git snapshot")
	}
	return nil
}
