package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
)

// Native messaging is how Chrome talks to this helper: it launches the
// binary and exchanges length-prefixed JSON over stdin/stdout.
//
// This is the transport that matters in production, and it is why the
// helper needs no port, no CORS, no Private Network Access header and no
// launchd agent — Chrome starts it on demand and closes it when done.
//
// Wire format: a 4-byte NATIVE-ENDIAN length, then that many bytes of
// UTF-8 JSON. Chrome's limits are 64 MiB for a message it sends and 1 MB
// for one it receives; note content travels in the generous direction, and
// replies here are small.
const (
	maxIncomingMessage = 64 << 20
	maxOutgoingMessage = 1 << 20
)

// request is what the extension sends. ID is echoed back so the extension
// can match a reply to its call.
type request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// ServeNativeMessaging runs the read/dispatch/write loop until stdin
// closes, which is Chrome's signal that it is finished with us.
func ServeNativeMessaging(api *API, in io.Reader, out io.Writer) error {
	for {
		req, err := readMessage(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // Chrome closed the port; a normal exit.
			}
			return err
		}

		reply := map[string]any{"id": req.ID, "ok": true}

		result, err := api.Dispatch(req.Method, req.Params)
		if err != nil {
			// Log to stderr, never stdout — stdout is the wire, and a
			// stray byte there corrupts the framing and hangs the channel.
			log.Printf("%s failed: %v", req.Method, err)
			reply["ok"] = false
			reply["error"] = err.Error()
			reply["code"] = CodeOf(err)
		} else {
			for k, v := range result {
				reply[k] = v
			}
		}

		if err := writeMessage(out, reply); err != nil {
			return err
		}
	}
}

func readMessage(in io.Reader) (request, error) {
	var header [4]byte
	if _, err := io.ReadFull(in, header[:]); err != nil {
		// A clean EOF between messages is the normal shutdown; a partial
		// header is not, and should be reported as such.
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return request{}, fmt.Errorf("truncated message header: %w", err)
		}
		return request{}, err
	}

	length := binary.NativeEndian.Uint32(header[:])
	if length == 0 {
		return request{}, fmt.Errorf("zero-length message")
	}
	if length > maxIncomingMessage {
		return request{}, fmt.Errorf("message of %d bytes exceeds the %d byte limit", length, maxIncomingMessage)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(in, body); err != nil {
		return request{}, fmt.Errorf("truncated message body: %w", err)
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return request{}, fmt.Errorf("message was not valid JSON: %w", err)
	}
	if req.Method == "" {
		return request{}, fmt.Errorf("message had no method")
	}
	return req, nil
}

func writeMessage(out io.Writer, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding reply: %w", err)
	}
	if len(body) > maxOutgoingMessage {
		// Chrome would drop this silently, so replace it with an error the
		// extension can actually see.
		body, _ = json.Marshal(map[string]any{
			"id": payload["id"], "ok": false, "code": CodeFailed,
			"error": fmt.Sprintf("reply of %d bytes exceeds Chrome's %d byte limit", len(body), maxOutgoingMessage),
		})
	}

	var header [4]byte
	binary.NativeEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := out.Write(header[:]); err != nil {
		return err
	}
	_, err = out.Write(body)
	return err
}
