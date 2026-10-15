package core

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mattn/go-shellwords"
)

// ParseCommand parses data only. It never invokes a shell or expands substitutions.
func ParseCommand(command string) (Rule, error) {
	r := Rule{RemoteHost: "localhost", Enabled: true, AutoReconnect: true}
	parser := shellwords.NewParser()
	parser.ParseEnv, parser.ParseBacktick = false, false
	args, err := parser.Parse(command)
	if err != nil {
		return r, err
	}
	if parser.Position >= 0 {
		return r, errors.New("shell operators and command chaining are not supported")
	}
	if len(args) < 3 || args[0] != "ssh" {
		return r, errors.New("expected: ssh -N -L LOCAL_PORT:REMOTE_HOST:REMOTE_PORT SSH_HOST")
	}
	var spec string
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-L" || strings.HasPrefix(a, "-L"):
			if spec != "" {
				return r, errors.New("import one -L forwarding at a time")
			}
			if a == "-L" {
				i++
				if i >= len(args) {
					return r, errors.New("-L requires a forwarding specification")
				}
				spec = args[i]
			} else {
				spec = a[2:]
			}
		case strings.HasPrefix(a, "-"):
			if len(a) < 2 || strings.Trim(a[1:], "NTn") != "" {
				return r, fmt.Errorf("unsupported option %q; configure SSH login ports, identities and jumps in ~/.ssh/config", a)
			}
		default:
			if r.SSHHost != "" {
				return r, errors.New("remote commands and shell operators are not supported")
			}
			r.SSHHost = a
		}
	}
	if spec == "" {
		return r, errors.New("command must contain one -L forwarding")
	}
	// Split outside IPv6 brackets.
	var parts []string
	start, depth := 0, 0
	for i, c := range spec {
		if c == '[' {
			depth++
		}
		if c == ']' {
			depth--
		}
		if depth < 0 {
			return r, errors.New("invalid IPv6 brackets")
		}
		if c == ':' && depth == 0 {
			parts = append(parts, spec[start:i])
			start = i + 1
		}
	}
	parts = append(parts, spec[start:])
	if depth != 0 {
		return r, errors.New("invalid IPv6 brackets")
	}
	if len(parts) == 4 {
		if parts[0] != "127.0.0.1" && parts[0] != "localhost" {
			return r, errors.New("MVP supports local binding on 127.0.0.1 only")
		}
		parts = parts[1:]
	}
	if len(parts) != 3 {
		return r, errors.New("expected LOCAL_PORT:REMOTE_HOST:REMOTE_PORT")
	}
	r.LocalPort, err = strconv.Atoi(parts[0])
	if err != nil {
		return r, errors.New("invalid local port")
	}
	r.RemotePort, err = strconv.Atoi(parts[2])
	if err != nil {
		return r, errors.New("invalid remote port")
	}
	r.RemoteHost = strings.Trim(parts[1], "[]")
	if strings.Contains(r.RemoteHost, ":") && net.ParseIP(r.RemoteHost) == nil {
		return r, errors.New("invalid remote IPv6 address")
	}
	r.Name = fmt.Sprintf("%s:%d", r.SSHHost, r.LocalPort)
	return r, ValidateRule(r)
}
