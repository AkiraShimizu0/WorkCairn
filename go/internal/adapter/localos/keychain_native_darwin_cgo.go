//go:build darwin && cgo

package localos

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework CoreFoundation -framework Security

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static CFMutableDictionaryRef workcairn_keychain_query(const char *service, const char *account) {
	CFStringRef serviceRef = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
	CFStringRef accountRef = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (serviceRef == NULL || accountRef == NULL) {
		if (serviceRef != NULL) CFRelease(serviceRef);
		if (accountRef != NULL) CFRelease(accountRef);
		return NULL;
	}
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (query != NULL) {
		CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
		CFDictionarySetValue(query, kSecAttrService, serviceRef);
		CFDictionarySetValue(query, kSecAttrAccount, accountRef);
		CFDictionarySetValue(query, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
	}
	CFRelease(serviceRef);
	CFRelease(accountRef);
	return query;
}

static OSStatus workcairn_keychain_upsert(const char *service, const char *account,
	const unsigned char *credential, size_t credentialLength) {
	CFMutableDictionaryRef query = workcairn_keychain_query(service, account);
	if (query == NULL) return errSecAllocate;
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, credential, (CFIndex)credentialLength);
	if (data == NULL) {
		CFRelease(query);
		return errSecAllocate;
	}
	CFMutableDictionaryRef add = CFDictionaryCreateMutableCopy(kCFAllocatorDefault, 0, query);
	if (add == NULL) {
		CFRelease(data);
		CFRelease(query);
		return errSecAllocate;
	}
	CFDictionarySetValue(add, kSecValueData, data);
	OSStatus status = SecItemAdd(add, NULL);
	if (status == errSecDuplicateItem) {
		const void *keys[] = { kSecValueData };
		const void *values[] = { data };
		CFDictionaryRef update = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 1,
			&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
		if (update == NULL) status = errSecAllocate;
		else {
			status = SecItemUpdate(query, update);
			CFRelease(update);
		}
	}
	CFRelease(add);
	CFRelease(data);
	CFRelease(query);
	return status;
}

static OSStatus workcairn_keychain_read(const char *service, const char *account,
	unsigned char **output, size_t *outputLength) {
	*output = NULL;
	*outputLength = 0;
	CFMutableDictionaryRef query = workcairn_keychain_query(service, account);
	if (query == NULL) return errSecAllocate;
	CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(query, &result);
	CFRelease(query);
	if (status != errSecSuccess) return status;
	if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
		if (result != NULL) CFRelease(result);
		return errSecDecode;
	}
	CFDataRef data = (CFDataRef)result;
	CFIndex length = CFDataGetLength(data);
	if (length <= 0) {
		CFRelease(result);
		return errSecDecode;
	}
	unsigned char *copy = malloc((size_t)length);
	if (copy == NULL) {
		CFRelease(result);
		return errSecAllocate;
	}
	memcpy(copy, CFDataGetBytePtr(data), (size_t)length);
	CFRelease(result);
	*output = copy;
	*outputLength = (size_t)length;
	return errSecSuccess;
}

static void workcairn_zero_free(void *value, size_t length) {
	if (value == NULL) return;
	volatile unsigned char *cursor = (volatile unsigned char *)value;
	while (length-- > 0) *cursor++ = 0;
	free(value);
}
*/
import "C"

import "unsafe"

const (
	errSecUnimplemented         = int32(C.errSecUnimplemented)
	errSecNotAvailable          = int32(C.errSecNotAvailable)
	errSecAuthFailed            = int32(C.errSecAuthFailed)
	errSecReadOnly              = int32(C.errSecReadOnly)
	errSecItemNotFound          = int32(C.errSecItemNotFound)
	errSecInteractionNotAllowed = int32(C.errSecInteractionNotAllowed)
	errSecInteractionRequired   = int32(C.errSecInteractionRequired)
	errSecDataNotAvailable      = int32(C.errSecDataNotAvailable)
	errSecDataNotModifiable     = int32(C.errSecDataNotModifiable)
	errSecNoSuchKeychain        = int32(C.errSecNoSuchKeychain)
	errSecNoAccessForItem       = int32(C.errSecNoAccessForItem)
	errSecDatabaseLocked        = int32(C.errSecDatabaseLocked)
	errSecMissingEntitlement    = int32(C.errSecMissingEntitlement)
)

func nativeKeychainUpsert(service, account string, credential []byte) int32 {
	serviceValue := C.CString(service)
	accountValue := C.CString(account)
	credentialValue := C.CBytes(credential)
	defer C.free(unsafe.Pointer(serviceValue))
	defer C.free(unsafe.Pointer(accountValue))
	defer C.workcairn_zero_free(credentialValue, C.size_t(len(credential)))
	return int32(C.workcairn_keychain_upsert(serviceValue, accountValue,
		(*C.uchar)(credentialValue), C.size_t(len(credential))))
}

func nativeKeychainRead(service, account string) ([]byte, int32) {
	serviceValue := C.CString(service)
	accountValue := C.CString(account)
	defer C.free(unsafe.Pointer(serviceValue))
	defer C.free(unsafe.Pointer(accountValue))
	var output *C.uchar
	var length C.size_t
	status := int32(C.workcairn_keychain_read(serviceValue, accountValue, &output, &length))
	if output == nil {
		return nil, status
	}
	defer C.workcairn_zero_free(unsafe.Pointer(output), length)
	return C.GoBytes(unsafe.Pointer(output), C.int(length)), status
}
