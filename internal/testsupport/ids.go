package testsupport

import "fmt"

// ID maps a fixture number to a stable UUID. Never use it for product identities.
func ID(n uint32) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
}
