// Package config parses startup settings without reading global flags or environment.
package config

import (
	"A3S/internal/objectstore"
	"errors"
	"flag"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	DataDir, Host, Token, TLSCert, TLSKey string
	LogLevel                              string
	Port                                  int
	Limits                                objectstore.Limits
	RequestTimeout, ShutdownTimeout       time.Duration
	AllowInsecureHTTP                     bool
}

func Parse(args []string, token string, out io.Writer) (Config, error) {
	c := Config{Token: token}
	flags := flag.NewFlagSet("triple-s", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.StringVar(&c.DataDir, "dir", "data", "Storage directory (single process, local filesystem)")
	flags.StringVar(&c.LogLevel, "log-level", "info", "JSON log level: debug, info, warn, error")
	flags.StringVar(&c.Host, "host", "127.0.0.1", "Listen IP address")
	flags.IntVar(&c.Port, "port", 8080, "Listen port")
	flags.Int64Var(&c.Limits.MaxObjectBytes, "max-object-bytes", 64<<20, "Maximum object size in bytes")
	flags.IntVar(&c.Limits.MaxConcurrentDownloads, "max-downloads", 16, "Maximum simultaneous downloads")
	flags.IntVar(&c.Limits.MaxConcurrentUploads, "max-uploads", 8, "Maximum simultaneous uploads")
	flags.DurationVar(&c.RequestTimeout, "request-timeout", 2*time.Minute, "Maximum request duration")
	flags.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", 30*time.Second, "Graceful shutdown deadline")
	flags.StringVar(&c.TLSCert, "tls-cert", "", "TLS certificate file")
	flags.StringVar(&c.TLSKey, "tls-key", "", "TLS private key file")
	flags.BoolVar(&c.AllowInsecureHTTP, "allow-insecure-http", false, "Allow non-loopback HTTP only behind a trusted TLS proxy")
	if err := flags.Parse(args); err != nil {
		return c, err
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, errors.New("invalid log level")
	}
	if flags.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, errors.New("storage directory is required")
	}
	if c.Port < 1024 || c.Port > 49151 {
		return c, errors.New("port must be between 1024 and 49151")
	}
	ip := net.ParseIP(c.Host)
	if ip == nil {
		return c, errors.New("host must be an IP address")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("tls-cert and tls-key must be supplied together")
	}
	if !ip.IsLoopback() && c.TLSCert == "" && !c.AllowInsecureHTTP {
		return c, errors.New("non-loopback HTTP requires TLS or explicit allow-insecure-http behind a trusted proxy")
	}
	if len(c.Token) < 32 || strings.ContainsAny(c.Token, " \t\r\n") {
		return c, errors.New("TRIPLE_S_TOKEN must contain at least 32 bytes and no whitespace")
	}
	if err := c.Limits.Validate(); err != nil {
		return c, err
	}
	if c.RequestTimeout <= 0 || c.RequestTimeout > 24*time.Hour || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > 24*time.Hour {
		return c, errors.New("timeouts must be positive and at most 24 hours")
	}
	absoluteDir, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.DataDir = filepath.Clean(absoluteDir)
	return c, nil
}
