//go:build darwin && !cgo

package localos

const (
	errSecUnimplemented         = int32(-4)
	errSecNotAvailable          = int32(-25291)
	errSecReadOnly              = int32(-25292)
	errSecAuthFailed            = int32(-25293)
	errSecItemNotFound          = int32(-25300)
	errSecInteractionNotAllowed = int32(-25308)
	errSecInteractionRequired   = int32(-25315)
	errSecDataNotAvailable      = int32(-25316)
	errSecDataNotModifiable     = int32(-25317)
	errSecNoSuchKeychain        = int32(-25294)
	errSecNoAccessForItem       = int32(-25243)
	errSecDatabaseLocked        = int32(-67869)
	errSecMissingEntitlement    = int32(-34018)
)

func nativeKeychainUpsert(string, string, []byte) int32 { return errSecUnimplemented }
func nativeKeychainRead(string, string) ([]byte, int32) { return nil, errSecUnimplemented }
