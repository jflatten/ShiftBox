//go:build !darwin || !cgo

package main

import "errors"

var errNativeHelper = errors.New("ShiftBox Local Helper requires a native macOS build")

type nativeHelperStore struct{}

func (nativeHelperStore) Load(string) (helperSession, error) { return helperSession{}, errNativeHelper }
func (nativeHelperStore) Save(string, helperSession) error   { return errNativeHelper }
func (nativeHelperStore) Remove(string) error                { return errNativeHelper }

func registerHelperLogin() error { return errNativeHelper }
