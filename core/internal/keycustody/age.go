package keycustody

import "github.com/retnd/retnd/core/internal/copycrypt"

// AgeFormatID is the format id ADR 0027 records per copy: the suite id of
// age/v1 in copycrypt's registry (ADR 0033 D1).
const AgeFormatID = copycrypt.AgeX25519ID

// AgeFormat is Format over age v1 (https://c2sp.org/age) with an X25519
// identity as the key. It is copycrypt's age/v1 suite adapter by alias
// (EPIC T, #1312): the adapter moved to copycrypt so the encrypt and
// decrypt halves live beside each other, and the name stays here so
// nothing that says keycustody.AgeFormat{} has to change. Custody only ever
// calls its Format methods; its Seal belongs to copycrypt's write path.
type AgeFormat = copycrypt.AgeX25519

// ErrNotAnIdentity is the refusal for a secret that is not an age X25519
// identity. It never quotes the secret. It is copycrypt's value, so
// errors.Is matches whichever package's name a caller uses.
var ErrNotAnIdentity = copycrypt.ErrNotAnIdentity

var _ Format = AgeFormat{}
