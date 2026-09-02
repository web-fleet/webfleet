package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Default Web Fleet host and port used by the shared project configuration
// pattern. Web Fleet's default listener is 127.0.0.1:7336; both remain
// configurable. The legacy WEBFLEET_LISTEN variable (and the service --listen
// flag) stays the durable source of the bootstrap address when no explicit
// host/port form is selected.
const (
	defaultHost = "127.0.0.1"
	defaultPort = "7336"
)

// flagProvided reports whether name was explicitly supplied on the command
// line. Standard flag strings cannot otherwise distinguish `--port ""` from an
// absent flag, and the contract requires empty CLI values to fail.
func flagProvided(fs *flag.FlagSet, name string) bool {
	provided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			provided = true
		}
	})
	return provided
}

// validatePort reports whether p is a valid TCP port: an integer from 1
// through 65535. It never silently falls back to a default.
func validatePort(p string) error {
	n, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be an integer from 1 through 65535; got %q", p)
	}
	return nil
}

// resolveHostPort computes the effective bind host and port. Precedence per
// field is CLI flag > environment variable > default. Values are trimmed once
// and the canonical trimmed form is returned, so surrounding whitespace cannot
// leak into the listener string. A port that is present but empty, malformed,
// zero, negative or greater than 65535 is an error; an empty host value is
// rejected rather than silently meaning "all interfaces".
func resolveHostPort(hostFlag, portFlag string, hostSet, portSet bool) (host, port string, err error) {
	host = defaultHost
	if hostSet {
		host = strings.TrimSpace(hostFlag)
		if host == "" {
			return "", "", errors.New("--host is set but empty")
		}
	} else if v, ok := os.LookupEnv("WEBFLEET_HOST"); ok {
		host = strings.TrimSpace(v)
		if host == "" {
			return "", "", errors.New("WEBFLEET_HOST is set but empty")
		}
	}
	port = defaultPort
	if portSet {
		if err := validatePort(portFlag); err != nil {
			return "", "", fmt.Errorf("--port: %w", err)
		}
		port = strings.TrimSpace(portFlag)
	} else if v, ok := os.LookupEnv("WEBFLEET_PORT"); ok {
		if err := validatePort(v); err != nil {
			return "", "", fmt.Errorf("WEBFLEET_PORT: %w", err)
		}
		port = strings.TrimSpace(v)
	}
	return host, port, nil
}

// resolveListener computes the effective HTTP listen address from the CLI
// flags and environment with the contract CLI > environment > default.
//
//   - explicit --listen wins and cannot be combined with explicit --host/--port;
//   - explicit --host and/or --port override the legacy WEBFLEET_LISTEN variable;
//   - with only environment variables, legacy WEBFLEET_LISTEN conflicts with
//     WEBFLEET_HOST/WEBFLEET_PORT rather than silently picking one;
//   - otherwise WEBFLEET_LISTEN is used, then host/port defaults.
//
// IPv6 hosts are bracketed via net.JoinHostPort.
func resolveListener(hostFlag, portFlag, listenFlag string, hostSet, portSet, listenSet bool) (string, error) {
	if listenSet {
		if hostSet || portSet {
			return "", errors.New("--listen cannot be combined with --host or --port")
		}
		if strings.TrimSpace(listenFlag) == "" {
			return "", errors.New("--listen is set but empty")
		}
		return listenFlag, nil
	}
	// Explicit --host/--port override the legacy WEBFLEET_LISTEN variable.
	if hostSet || portSet {
		host, port, err := resolveHostPort(hostFlag, portFlag, hostSet, portSet)
		if err != nil {
			return "", err
		}
		return net.JoinHostPort(host, port), nil
	}
	// Only environment variables are involved: legacy WEBFLEET_LISTEN versus the
	// new WEBFLEET_HOST/WEBFLEET_PORT forms must not silently pick one.
	if v, ok := os.LookupEnv("WEBFLEET_LISTEN"); ok {
		if _, hasHost := os.LookupEnv("WEBFLEET_HOST"); hasHost {
			return "", errors.New("WEBFLEET_LISTEN cannot be combined with WEBFLEET_HOST")
		}
		if _, hasPort := os.LookupEnv("WEBFLEET_PORT"); hasPort {
			return "", errors.New("WEBFLEET_LISTEN cannot be combined with WEBFLEET_PORT")
		}
		if strings.TrimSpace(v) == "" {
			return "", errors.New("WEBFLEET_LISTEN is set but empty")
		}
		return v, nil
	}
	host, port, err := resolveHostPort(hostFlag, portFlag, hostSet, portSet)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}
