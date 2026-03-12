package app

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	systemdListenFdsStart = 3
)

// ActivatedListenerInfo describes one activated listener from systemd.
type ActivatedListenerInfo struct {
	Name     string
	Listener net.Listener
}

// ActivatedListeners returns all socket-activated listeners passed by systemd.
// If socket activation is not active for this process, it returns nil, nil.
func ActivatedListeners() ([]ActivatedListenerInfo, error) {
	pidRaw := strings.TrimSpace(os.Getenv("LISTEN_PID"))
	fdsRaw := strings.TrimSpace(os.Getenv("LISTEN_FDS"))
	if pidRaw == "" || fdsRaw == "" {
		return nil, nil
	}

	pid, err := strconv.Atoi(pidRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid LISTEN_PID %q: %w", pidRaw, err)
	}
	if pid != os.Getpid() {
		return nil, nil
	}

	fdCount, err := strconv.Atoi(fdsRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid LISTEN_FDS %q: %w", fdsRaw, err)
	}
	if fdCount <= 0 {
		return nil, nil
	}

	names := parseFDNames(strings.TrimSpace(os.Getenv("LISTEN_FDNAMES")), fdCount)
	infos := make([]ActivatedListenerInfo, 0, fdCount)
	for i := 0; i < fdCount; i++ {
		fd := uintptr(systemdListenFdsStart + i)
		if prepErr := prepareActivatedFD(fd); prepErr != nil {
			return nil, fmt.Errorf("prepare activated fd %d: %w", fd, prepErr)
		}
		file := os.NewFile(fd, fmt.Sprintf("systemd-listener-%d", fd))
		if file == nil {
			return nil, fmt.Errorf("failed to access activated fd %d", fd)
		}

		listener, listenErr := net.FileListener(file)
		_ = file.Close()
		if listenErr != nil {
			return nil, fmt.Errorf("fd %d is not a supported listener: %w", fd, listenErr)
		}

		infos = append(infos, ActivatedListenerInfo{
			Name:     names[i],
			Listener: listener,
		})
	}

	return infos, nil
}

// PickActivatedListener selects a listener by preferred name or expected TCP port.
// Returns nil,false,nil if no matching activated listener is found.
func PickActivatedListener(infos []ActivatedListenerInfo, preferredName, expectedAddr string) (net.Listener, bool, error) {
	if len(infos) == 0 {
		return nil, false, nil
	}

	if preferredName != "" {
		for _, info := range infos {
			if info.Name == preferredName {
				closeOthers(infos, info.Listener)
				return info.Listener, true, nil
			}
		}
	}

	expectedPort := tcpPort(expectedAddr)
	if expectedPort != "" {
		for _, info := range infos {
			if tcpPort(info.Listener.Addr().String()) == expectedPort {
				closeOthers(infos, info.Listener)
				return info.Listener, true, nil
			}
		}
	}

	// No deterministic match, use first one and close the rest.
	chosen := infos[0].Listener
	closeOthers(infos, chosen)

	return chosen, true, nil
}

func parseFDNames(raw string, count int) []string {
	out := make([]string, count)
	if raw == "" {
		return out
	}

	parts := strings.Split(raw, ":")
	for i := 0; i < len(parts) && i < count; i++ {
		out[i] = strings.TrimSpace(parts[i])
	}

	return out
}

func closeOthers(infos []ActivatedListenerInfo, keep net.Listener) {
	for _, info := range infos {
		if info.Listener == nil || info.Listener == keep {
			continue
		}
		_ = info.Listener.Close()
	}
}

func tcpPort(addr string) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return ""
	}

	normalized := trimmed
	if strings.HasPrefix(normalized, ":") {
		normalized = "0.0.0.0" + normalized
	}

	_, port, err := net.SplitHostPort(normalized)
	if err != nil {
		return ""
	}

	return port
}
