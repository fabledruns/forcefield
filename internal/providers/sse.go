package providers

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// sseReader reads a Server-Sent Events body and invokes fn once per data
// payload. It implements the small subset of the SSE spec every supported
// provider uses: "data:" lines accumulated until a blank line dispatches
// them (joined with newlines when a server splits one event across
// several lines), ":" comment lines ignored, "event:"/"id:" fields
// ignored because every adapter keys off the JSON payload itself.
//
// fn returning an error stops reading and returns that error; io.EOF at a
// clean end of body is reported as a nil return.
func sseReader(ctx context.Context, r io.Reader, fn func(data string) error) error {
	scanner := bufio.NewScanner(r)
	// SSE events are small JSON documents, but tool-call argument deltas
	// can make individual data lines large; 1 MiB per line is generous.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		return fn(payload)
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// other SSE fields (event:, id:, retry:) carry nothing the
			// adapters need.
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
