package main

import (
	"encoding/json"
	"io"
	"os"
)

// streams is where a command writes its text and progress. The CLI passes the
// terminal's; the panel, which runs commands from concurrent requests, its own.
type streams struct{ Out, Err io.Writer }

func stdStreams() streams { return streams{os.Stdout, os.Stderr} }

// jsonStreams is for --json: the text is dropped, and the result printed as JSON instead.
func jsonStreams() streams { return streams{io.Discard, os.Stderr} }

// quietStreams drops everything: the panel's queries want only the result.
func quietStreams() streams { return streams{io.Discard, io.Discard} }

// encodeJSON is how every command prints its --json result.
func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
