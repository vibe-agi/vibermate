package egressprofile

import (
	"bytes"
	"encoding/json"
	"io"
)

// Choice is comparable authored configuration, never transport authority.
type Choice struct {
	kind    uint8
	profile ProfileRevision
}

func Inherit() Choice { return Choice{kind: 1} }
func Explicit(profile ProfileRevision) (Choice, error) {
	if err := validateSelectionProfile(profile); err != nil {
		return Choice{}, err
	}
	return Choice{kind: 2, profile: profile}, nil
}
func validateSelectionProfile(profile ProfileRevision) error {
	if profile.Validate() != nil || profile.ID == DirectID && !profile.Equal(Direct()) {
		return ErrInvalidProfile
	}
	return nil
}
func (choice Choice) Validate() error {
	switch choice.kind {
	case 1:
		if choice.profile == (ProfileRevision{}) {
			return nil
		}
	case 2:
		return validateSelectionProfile(choice.profile)
	}
	return ErrInvalidProfile
}
func (choice Choice) ExplicitProfile() (ProfileRevision, bool) {
	return choice.profile, choice.kind == 2 && choice.Validate() == nil
}
func (choice Choice) MarshalJSON() ([]byte, error) {
	if err := choice.Validate(); err != nil {
		return nil, err
	}
	if choice.kind == 1 {
		return []byte(`{"mode":"inherit"}`), nil
	}
	return json.Marshal(choice.profile)
}

// strictMembers rejects duplicate (including escaped duplicate), case aliases,
// unknown and missing names rather than relying on encoding/json field matching.
func strictMembers(data []byte, allowed, required []string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, ErrInvalidProfile
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil, ErrInvalidProfile
		}
		known := false
		for _, candidate := range allowed {
			if candidate == name {
				known = true
				break
			}
		}
		if _, exists := fields[name]; exists || !known {
			return nil, ErrInvalidProfile
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, ErrInvalidProfile
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, ErrInvalidProfile
		}
		fields[name] = raw
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, ErrInvalidProfile
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, ErrInvalidProfile
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return nil, ErrInvalidProfile
		}
	}
	return fields, nil
}
func (choice *Choice) UnmarshalJSON(data []byte) error {
	if choice == nil {
		return ErrInvalidProfile
	}
	*choice = Choice{}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return ErrInvalidProfile
	}
	if _, ok := raw["mode"]; ok {
		fields, err := strictMembers(data, []string{"mode"}, []string{"mode"})
		if err != nil || string(fields["mode"]) != `"inherit"` {
			return ErrInvalidProfile
		}
		*choice = Inherit()
		return nil
	}
	fields, err := strictMembers(data, []string{"id", "revision", "displayName", "policy", "publishedAt"}, []string{"id", "revision", "displayName", "policy", "publishedAt"})
	if err != nil {
		return err
	}
	policy, err := strictMembers(fields["policy"], []string{"proxy", "resolver", "directDomains"}, []string{"proxy", "resolver"})
	if err != nil {
		return err
	}
	if _, err = strictMembers(policy["proxy"], []string{"kind", "endpoint", "credentialRef", "credentialRevision"}, []string{"kind"}); err != nil {
		return err
	}
	if _, err = strictMembers(policy["resolver"], []string{"kind", "dohUrl", "transport"}, []string{"kind", "transport"}); err != nil {
		return err
	}
	var profile ProfileRevision
	if json.Unmarshal(data, &profile) != nil {
		return ErrInvalidProfile
	}
	parsed, err := Explicit(profile)
	if err != nil {
		return err
	}
	*choice = parsed
	return nil
}
