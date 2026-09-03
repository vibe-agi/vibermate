//go:build vibermate_native_secrets && darwin

package hostsecret

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

// vibermateDictionarySet keeps the CoreFoundation types on the C side. Going
// through unsafe.Pointer in Go would be a conversion from a uintptr-shaped
// handle, which is exactly the pattern the vet check exists to catch.
static void vibermateDictionarySet(
    CFMutableDictionaryRef dictionary,
    CFStringRef key,
    CFTypeRef value
) {
    CFDictionarySetValue(dictionary, key, value);
}

static CFTypeRef vibermateDictionaryGet(
    CFDictionaryRef dictionary,
    CFStringRef key
) {
    return CFDictionaryGetValue(dictionary, key);
}
*/
import "C"

import (
	"encoding/binary"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// revisionBytes is the fixed width of the revision stored in the item's
// application-specific attribute.
const revisionBytes = 8

// query builds the attributes that identify exactly one item.
func (store *KeychainStore) query(
	reference secretstore.Reference,
) C.CFMutableDictionaryRef {
	query := C.CFDictionaryCreateMutable(
		C.kCFAllocatorDefault,
		0,
		&C.kCFTypeDictionaryKeyCallBacks,
		&C.kCFTypeDictionaryValueCallBacks,
	)
	C.vibermateDictionarySet(query, C.kSecClass, C.CFTypeRef(C.kSecClassGenericPassword))
	service := cfString(store.service)
	defer C.CFRelease(C.CFTypeRef(service))
	C.vibermateDictionarySet(query, C.kSecAttrService, C.CFTypeRef(service))
	account := cfString(reference.String())
	defer C.CFRelease(C.CFTypeRef(account))
	C.vibermateDictionarySet(query, C.kSecAttrAccount, C.CFTypeRef(account))
	return query
}

// copyItem reads one item. Asking for data and asking for attributes are
// different acts to the operating system, and only the first is a secret read.
func (store *KeychainStore) copyItem(
	reference secretstore.Reference,
	withData bool,
) ([]byte, secretstore.Revision, error) {
	query := store.query(reference)
	defer C.CFRelease(C.CFTypeRef(query))
	preventAuthenticationUI(query)
	C.vibermateDictionarySet(query, C.kSecMatchLimit, C.CFTypeRef(C.kSecMatchLimitOne))
	C.vibermateDictionarySet(query, C.kSecReturnAttributes, C.CFTypeRef(C.kCFBooleanTrue))
	if withData {
		C.vibermateDictionarySet(query, C.kSecReturnData, C.CFTypeRef(C.kCFBooleanTrue))
	}
	var result C.CFTypeRef
	status := C.SecItemCopyMatching(C.CFDictionaryRef(query), &result)
	if status == C.errSecItemNotFound {
		return nil, 0, secretstore.ErrNotFound
	}
	if err := keychainError(status); err != nil {
		return nil, 0, err
	}
	defer C.CFRelease(result)

	attributes := C.CFDictionaryRef(result)
	revision, err := revisionFrom(attributes)
	if err != nil {
		return nil, 0, err
	}
	if !withData {
		return nil, revision, nil
	}
	data := C.vibermateDictionaryGet(attributes, C.kSecValueData)
	if data == 0 {
		return nil, 0, secretstore.ErrUnavailable
	}
	return cfDataBytes(C.CFDataRef(data)), revision, nil
}

func (store *KeychainStore) addItem(
	reference secretstore.Reference,
	value []byte,
	revision secretstore.Revision,
) error {
	attributes := store.query(reference)
	defer C.CFRelease(C.CFTypeRef(attributes))
	data := cfData(value)
	defer C.CFRelease(C.CFTypeRef(data))
	C.vibermateDictionarySet(attributes, C.kSecValueData, C.CFTypeRef(data))
	generic := cfData(encodeRevision(revision))
	defer C.CFRelease(C.CFTypeRef(generic))
	C.vibermateDictionarySet(attributes, C.kSecAttrGeneric, C.CFTypeRef(generic))
	// The item is readable only while this account is logged in and the
	// keychain is unlocked, and it never leaves this machine.
	C.vibermateDictionarySet(
		attributes,
		C.kSecAttrAccessible,
		C.CFTypeRef(C.kSecAttrAccessibleWhenUnlockedThisDeviceOnly),
	)
	return keychainError(C.SecItemAdd(C.CFDictionaryRef(attributes), nil))
}

func (store *KeychainStore) updateItem(
	reference secretstore.Reference,
	value []byte,
	expectedRevision secretstore.Revision,
	nextRevision secretstore.Revision,
) error {
	query := store.query(reference)
	defer C.CFRelease(C.CFTypeRef(query))
	preventAuthenticationUI(query)
	expected := cfData(encodeRevision(expectedRevision))
	defer C.CFRelease(C.CFTypeRef(expected))
	// kSecAttrGeneric is part of the search dictionary, so SecItemUpdate only
	// matches the exact physical revision observed by Replace. Two processes
	// racing from one revision cannot both update the item.
	C.vibermateDictionarySet(query, C.kSecAttrGeneric, C.CFTypeRef(expected))
	changes := C.CFDictionaryCreateMutable(
		C.kCFAllocatorDefault,
		0,
		&C.kCFTypeDictionaryKeyCallBacks,
		&C.kCFTypeDictionaryValueCallBacks,
	)
	defer C.CFRelease(C.CFTypeRef(changes))
	data := cfData(value)
	defer C.CFRelease(C.CFTypeRef(data))
	C.vibermateDictionarySet(changes, C.kSecValueData, C.CFTypeRef(data))
	generic := cfData(encodeRevision(nextRevision))
	defer C.CFRelease(C.CFTypeRef(generic))
	C.vibermateDictionarySet(changes, C.kSecAttrGeneric, C.CFTypeRef(generic))
	status := C.SecItemUpdate(
		C.CFDictionaryRef(query),
		C.CFDictionaryRef(changes),
	)
	if status == C.errSecItemNotFound {
		// The item was deleted or its revision advanced between the attribute
		// read and this conditional update. Both are CAS conflicts.
		return secretstore.ErrRevisionConflict
	}
	return keychainError(status)
}

// preventAuthenticationUI makes every background Keychain query fail with a
// typed store error instead of presenting (or waiting forever behind) an OS
// authentication dialog. ViberMate's sidecar has no interactive UI surface;
// the desktop control plane can report and repair an unavailable secret, but a
// secret lookup must never stall unrelated proxy traffic.
func preventAuthenticationUI(query C.CFMutableDictionaryRef) {
	C.vibermateDictionarySet(
		query,
		C.kSecUseAuthenticationUI,
		C.CFTypeRef(C.kSecUseAuthenticationUIFail),
	)
}

func revisionFrom(attributes C.CFDictionaryRef) (secretstore.Revision, error) {
	generic := C.vibermateDictionaryGet(attributes, C.kSecAttrGeneric)
	if generic == 0 {
		// An item written by something other than this store has no revision
		// this store can trust. Treating it as revision zero would let a
		// blind write replace it.
		return 0, secretstore.ErrUnavailable
	}
	raw := cfDataBytes(C.CFDataRef(generic))
	defer wipe(raw)
	if len(raw) != revisionBytes {
		return 0, secretstore.ErrUnavailable
	}
	stored := binary.BigEndian.Uint64(raw)
	if stored > uint64(secretstore.MaxRevision) {
		return 0, secretstore.ErrUnavailable
	}
	return secretstore.Revision(stored), nil
}

func encodeRevision(revision secretstore.Revision) []byte {
	encoded := make([]byte, revisionBytes)
	binary.BigEndian.PutUint64(encoded, uint64(revision))
	return encoded
}

// keychainError maps an OSStatus onto this package's vocabulary. An
// unrecognized status is unavailable rather than "not found": a store that
// cannot answer must not read as a store with nothing in it.
func keychainError(status C.OSStatus) error {
	switch status {
	case C.errSecSuccess:
		return nil
	case C.errSecItemNotFound:
		return secretstore.ErrNotFound
	case C.errSecInteractionNotAllowed:
		return secretstore.ErrLocked
	case C.errSecAuthFailed, C.errSecUserCanceled:
		return secretstore.ErrDenied
	case C.errSecDuplicateItem:
		return secretstore.ErrRevisionConflict
	default:
		return secretstore.ErrUnavailable
	}
}

func cfString(value string) C.CFStringRef {
	bytes := []byte(value)
	if len(bytes) == 0 {
		return C.CFStringCreateWithBytes(
			C.kCFAllocatorDefault,
			nil,
			0,
			C.kCFStringEncodingUTF8,
			C.false,
		)
	}
	return C.CFStringCreateWithBytes(
		C.kCFAllocatorDefault,
		(*C.UInt8)(unsafe.Pointer(&bytes[0])),
		C.CFIndex(len(bytes)),
		C.kCFStringEncodingUTF8,
		C.false,
	)
}

func cfData(value []byte) C.CFDataRef {
	if len(value) == 0 {
		return C.CFDataCreate(C.kCFAllocatorDefault, nil, 0)
	}
	return C.CFDataCreate(
		C.kCFAllocatorDefault,
		(*C.UInt8)(unsafe.Pointer(&value[0])),
		C.CFIndex(len(value)),
	)
}

func cfDataBytes(data C.CFDataRef) []byte {
	length := C.CFDataGetLength(data)
	if length <= 0 {
		return nil
	}
	pointer := C.CFDataGetBytePtr(data)
	return C.GoBytes(unsafe.Pointer(pointer), C.int(length))
}

// wipe clears a copy of secret bytes this package made. It cannot reach the
// operating system's own copy, and does not claim to.
func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
