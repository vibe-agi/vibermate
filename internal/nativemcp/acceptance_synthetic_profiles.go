//go:build vibermate_acceptance_synthetic

package nativemcp

import (
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// AcceptanceSyntheticContentProfiles classifies only the fixed private
// synthetic_read/v1 definition. This tagged factory is not a production
// classifier and supplies no execution or approval authority.
func AcceptanceSyntheticContentProfiles() ContentProfiles { return acceptanceSyntheticProfiles{} }

type acceptanceSyntheticProfiles struct{}

func (acceptanceSyntheticProfiles) Resolve(contract environment.ClientToolContract, definition nativetool.CatalogDefinition) (ContentProfile, bool) {
	fixed := nativetool.Digest{0x9e, 0xc7, 0x4d, 0xa5, 0x4c, 0xc7, 0xf8, 0x65, 0xd2, 0x2d, 0xc1, 0xd0, 0x20, 0xab, 0xf5, 0xa6, 0xd9, 0xa7, 0xdf, 0x1c, 0xd7, 0xa1, 0xdb, 0x07, 0xd5, 0x09, 0xc6, 0x39, 0x43, 0x29, 0x06, 0x5f}
	if contract != environment.ClientToolContractCodexLegacy || definition.Digest() != fixed {
		return ContentProfile{}, false
	}
	return ContentProfile{contract: environment.ClientToolContractCodexLegacy, definition: fixed, arguments: []string{"/proof", "/credentialId"}, result: []string{"/credentialId", "/structuredContent/credentialId"}}, true
}

// AcceptanceSyntheticContentInput is a closed, tagged ownership fixture. It is
// absent from production builds and never supplies execution authority.
func AcceptanceSyntheticContentInput(identity nativetool.ContentIdentity, part nativetool.ContentPart, raw []byte) ContentInput {
	return ContentInput{identity: identity, observedAt: identity.CreatedAt, part: part, raw: raw, profile: ContentProfile{contract: environment.ClientToolContractCodexLegacy, definition: nativetool.Digest{1}}, limits: DefaultLimits()}
}
