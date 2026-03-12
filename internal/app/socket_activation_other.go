//go:build !unix

package app

func prepareActivatedFD(_ uintptr) error {
	return nil
}
