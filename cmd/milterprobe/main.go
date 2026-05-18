// Package main sends a single Milter frame through doppelgaenger.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"doppelgaenger/internal/protocol"
)

type probeResult struct {
	Decision string `json:"decision"`
	Command  string `json:"command"`
	RawBytes int    `json:"raw_bytes"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:19999", "doppelgaenger milter address")
	command := flag.String("command", "c", "Milter command byte")
	payload := flag.String("payload", "e2e", "Milter payload")
	expectDecision := flag.String("expect-decision", "accept", "expected response decision")
	timeout := flag.Duration("timeout", 2*time.Second, "network timeout")

	flag.Parse()

	result, err := runProbe(*addr, firstByte(*command), []byte(*payload), *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe failed: %v\n", err)
		os.Exit(1)
	}

	if result.Decision != *expectDecision {
		fmt.Fprintf(os.Stderr, "expected decision %q, got %q\n", *expectDecision, result.Decision)
		os.Exit(1)
	}

	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode result: %v\n", err)
		os.Exit(1)
	}
}

func runProbe(addr string, command byte, payload []byte, timeout time.Duration) (probeResult, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return probeResult{}, err
	}
	defer func() {
		_ = conn.Close()
	}()

	if deadlineErr := conn.SetDeadline(time.Now().Add(timeout)); deadlineErr != nil {
		return probeResult{}, deadlineErr
	}

	if _, err := conn.Write(encodeMilterFrame(command, payload)); err != nil {
		return probeResult{}, err
	}

	frame, err := protocol.ReadFrame(conn)
	if err != nil {
		return probeResult{}, err
	}

	return probeResult{
		Decision: decisionForCommand(frame.Command),
		Command:  string(frame.Command),
		RawBytes: len(frame.Raw),
	}, nil
}

func firstByte(value string) byte {
	if value == "" {
		return 'c'
	}

	return value[0]
}

func decisionForCommand(command byte) string {
	switch command {
	case 'a':
		return "accept"
	case 'r':
		return "reject"
	case 't':
		return "tempfail"
	case 'd':
		return "discard"
	case 'c':
		return "continue"
	default:
		return "unknown"
	}
}

func encodeMilterFrame(command byte, payload []byte) []byte {
	frameLen := 1 + len(payload)
	buffer := make([]byte, 4+frameLen)
	binary.BigEndian.PutUint32(buffer[:4], uint32(frameLen))
	buffer[4] = command
	copy(buffer[5:], payload)

	return buffer
}
