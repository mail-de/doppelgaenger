// Package main starts a small fake Milter backend used by E2E tests.
package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"doppelgaenger/internal/protocol"
)

type milterLogger struct {
	mu   sync.Mutex
	file *os.File
}

type frameLog struct {
	Time          string `json:"time"`
	Remote        string `json:"remote"`
	Command       string `json:"command"`
	PayloadBase64 string `json:"payload_base64"`
	RawBytes      int    `json:"raw_bytes"`
	Decision      string `json:"decision"`
}

func main() {
	addr := flag.String("listen", "127.0.0.1:19997", "listen address")
	decision := flag.String("decision", "accept", "response decision")
	logFile := flag.String("log-file", "", "JSONL log file")

	flag.Parse()

	responseCommand, err := commandForDecision(*decision)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	logger, err := newMilterLogger(*logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logger.close()

	if err := serve(*addr, *decision, responseCommand, logger); err != nil {
		fmt.Fprintf(os.Stderr, "fake milter failed: %v\n", err)
		os.Exit(1)
	}
}

func commandForDecision(decision string) (byte, error) {
	switch decision {
	case "accept":
		return 'a', nil
	case "reject":
		return 'r', nil
	case "tempfail":
		return 't', nil
	case "discard":
		return 'd', nil
	case "continue":
		return 'c', nil
	default:
		return 0, fmt.Errorf("unsupported decision %q", decision)
	}
}

func newMilterLogger(path string) (*milterLogger, error) {
	if path == "" {
		return &milterLogger{}, nil
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	return &milterLogger{file: file}, nil
}

func (l *milterLogger) close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

func serve(addr, decision string, responseCommand byte, logger *milterLogger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}

		go handleConn(conn, decision, responseCommand, logger)
	}
}

func handleConn(conn net.Conn, decision string, responseCommand byte, logger *milterLogger) {
	defer func() {
		_ = conn.Close()
	}()

	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			return
		}

		logger.write(frameLog{
			Time:          time.Now().UTC().Format(time.RFC3339Nano),
			Remote:        conn.RemoteAddr().String(),
			Command:       string(frame.Command),
			PayloadBase64: base64.StdEncoding.EncodeToString(frame.Payload),
			RawBytes:      len(frame.Raw),
			Decision:      decision,
		})

		if _, err := conn.Write(encodeMilterFrame(responseCommand, nil)); err != nil {
			return
		}
	}
}

func (l *milterLogger) write(entry frameLog) {
	if l.file == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	_ = json.NewEncoder(l.file).Encode(entry)
}

func encodeMilterFrame(command byte, payload []byte) []byte {
	frameLen := 1 + len(payload)
	buffer := make([]byte, 4+frameLen)
	binary.BigEndian.PutUint32(buffer[:4], uint32(frameLen))
	buffer[4] = command
	copy(buffer[5:], payload)

	return buffer
}
