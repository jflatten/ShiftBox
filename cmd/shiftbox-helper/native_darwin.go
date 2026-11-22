//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Foundation -framework Security -framework ServiceManagement
#include <Security/Security.h>
#include <stdint.h>
#include <stdlib.h>

int32_t shiftbox_keychain_read(const char *origin, void **value, size_t *length);
int32_t shiftbox_keychain_save(const char *origin, const void *value, size_t length);
int32_t shiftbox_keychain_remove(const char *origin);
int shiftbox_login_item(char **message);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"unsafe"
)

type nativeHelperStore struct{}

func (nativeHelperStore) Load(origin string) (helperSession, error) {
	account := C.CString(origin)
	defer C.free(unsafe.Pointer(account))
	var value unsafe.Pointer
	var length C.size_t
	status := C.shiftbox_keychain_read(account, &value, &length)
	if status == C.errSecItemNotFound {
		return helperSession{}, errNoHelperCredential
	}
	if status != C.errSecSuccess {
		return helperSession{}, fmt.Errorf("read workspace credential from macOS Keychain (status %d)", status)
	}
	defer C.free(value)
	// The native bridge rejects oversized values and returns owned memory.
	data := unsafe.Slice((*byte)(value), int(length))
	defer clear(data)
	var session helperSession
	if err := json.Unmarshal(data, &session); err != nil {
		return helperSession{}, errors.New("invalid scoped workspace credential in macOS Keychain")
	}
	return session, nil
}

func (nativeHelperStore) Save(origin string, session helperSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	defer clear(data)
	account := C.CString(origin)
	defer C.free(unsafe.Pointer(account))
	status := C.shiftbox_keychain_save(account, unsafe.Pointer(unsafe.SliceData(data)), C.size_t(len(data)))
	if status != C.errSecSuccess {
		return fmt.Errorf("save workspace credential in macOS Keychain (status %d)", status)
	}
	return nil
}

func (nativeHelperStore) Remove(origin string) error {
	account := C.CString(origin)
	defer C.free(unsafe.Pointer(account))
	status := C.shiftbox_keychain_remove(account)
	if status != C.errSecSuccess && status != C.errSecItemNotFound {
		return fmt.Errorf("remove workspace credential from macOS Keychain (status %d)", status)
	}
	return nil
}

func registerHelperLogin() error {
	var message *C.char
	result := C.shiftbox_login_item(&message)
	if message != nil {
		defer C.free(unsafe.Pointer(message))
		return errors.New(C.GoString(message))
	}
	if result != 0 {
		return errors.New("macOS did not register the ShiftBox login item")
	}
	return nil
}
