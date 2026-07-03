// Command convert is a one-shot migration tool: it reads the legacy Node
// data/config.js, extracts only the `devices` array (no secrets), and writes it
// as data/devices.yaml for the Go service.
//
//	go run ./tools/convert data/config.js data/devices.yaml
package main

import (
	"fmt"
	"os"
	"strings"

	json5 "github.com/titanous/json5"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "convert:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	in, out := "data/config.js", "data/devices.yaml"
	if len(args) > 1 {
		in = args[1]
	}
	if len(args) > 2 {
		out = args[2]
	}

	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}

	// Strip the `module.exports =` wrapper: keep from the first '{' to the
	// last '}'.
	s := string(raw)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return fmt.Errorf("could not find object literal in %s", in)
	}
	obj := s[start : end+1]

	var cfg map[string]any
	if err := json5.Unmarshal([]byte(obj), &cfg); err != nil {
		return fmt.Errorf("parse %s as JSON5: %w", in, err)
	}

	devices, ok := cfg["devices"]
	if !ok {
		return fmt.Errorf("no `devices` key in %s", in)
	}

	doc := map[string]any{"devices": devices}
	var buf strings.Builder
	buf.WriteString("# Device catalog generated from the legacy data/config.js.\n")
	buf.WriteString("# Secrets live in env / .env — never here.\n\n")

	enc := yaml.NewEncoder(&stringWriter{&buf})
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	enc.Close()

	if err := os.WriteFile(out, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	n := 0
	if arr, ok := devices.([]any); ok {
		n = len(arr)
	}
	fmt.Printf("wrote %d devices to %s\n", n, out)
	return nil
}

type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
