//go:build unix

package app

import "golang.org/x/sys/unix"

func prepareActivatedFD(fd uintptr) error {
	flags, err := unix.FcntlInt(fd, unix.F_GETFD, 0)
	if err != nil {
		return err
	}
	flags &^= unix.FD_CLOEXEC
	_, err = unix.FcntlInt(fd, unix.F_SETFD, flags)
	return err
}
