package clientadapter

// A catalog declares an identity, never an expression.
type Signer struct {
	SigningIdentifier string
	TeamID            string
}

func builtIn() Signer {
	return Signer{SigningIdentifier: "com.example.app", TeamID: "ABCDE12345"}
}
