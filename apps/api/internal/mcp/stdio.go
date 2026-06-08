package mcp

import (
	"bufio"
	"bytes"
	"context"
	"io"
)

// stdioMaxLineBytes caps a single inbound JSON-RPC line. Requests are small
// (a tool name + a few args), so 1 MiB is generous and bounds memory if a peer
// sends a pathological line with no newline.
const stdioMaxLineBytes = 1 << 20

// ServeStdio runs the MCP server over a newline-delimited JSON-RPC stream
// (the MCP stdio transport): each inbound message is one line on `in`, each
// response is one line on `out`. It blocks until `in` reaches EOF, `ctx` is
// cancelled, or a write fails.
//
// Logging must go to stderr (never `out`) or it will corrupt the protocol
// stream — the cmd/mcp binary ensures this via the default slog stderr handler.
func ServeStdio(ctx context.Context, srv *Server, in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 64*1024)
	writer := bufio.NewWriter(out)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		line, readErr := readLine(reader, stdioMaxLineBytes)
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			resp, _ := srv.HandleMessage(ctx, msg)
			if resp != nil {
				if _, err := writer.Write(resp); err != nil {
					return err
				}
				if err := writer.WriteByte('\n'); err != nil {
					return err
				}
				if err := writer.Flush(); err != nil {
					return err
				}
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// readLine reads one '\n'-terminated line, returning the bytes read so far even
// when the terminating error is EOF (so a final line without a trailing newline
// is still processed). It errors if the line exceeds max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return buf[:max], errLineTooLong
		}
		if err == bufio.ErrBufferFull {
			// Line longer than the bufio buffer; keep accumulating.
			continue
		}
		return buf, err
	}
}

// errLineTooLong is returned when an inbound line exceeds stdioMaxLineBytes.
var errLineTooLong = errorString("inbound MCP line exceeds size limit")

type errorString string

func (e errorString) Error() string { return string(e) }
