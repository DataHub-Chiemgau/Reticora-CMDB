package crypto

// WrapUnmarked wraps dek with the current master key of e without the key
// id marker, like the DEKs of before the key ids (TestMasterKeyRotation).
func WrapUnmarked(e *EnvelopeEncryptor, dek []byte) ([]byte, error) {
	return encrypt(e.masterKey, dek)
}
