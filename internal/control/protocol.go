package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/keshon/3xui-reality-stack/internal/deployment"
)

const ProtocolVersion = 1
const maxFrame = 64 << 10

type request struct {
	Version int             `json:"version"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type applyParams struct {
	ReviewID  string `json:"review_id"`
	Confirmed bool   `json:"confirmed"`
}
type operationParams struct {
	OperationID string `json:"operation_id"`
}

type configureParams struct {
	Changes map[string]string `json:"changes"`
}

type initializationParams struct {
	Input json.RawMessage `json:"input"`
}

func supportedMethods(reader Reader) []string {
	if capable, ok := reader.(interface{ Methods() []string }); ok {
		return capable.Methods()
	}
	return []string{"inspect", "plan", "runtime"}
}

type capabilities struct {
	Version  int      `json:"version"`
	Methods  []string `json:"methods"`
	ReadOnly bool     `json:"read_only"`
}

func readFrame(in *bufio.Reader) ([]byte, error) {
	data, err := in.ReadSlice('\n')
	if err == io.EOF && len(data) == 0 {
		return nil, io.EOF
	}
	if err != nil || len(data) > maxFrame {
		return nil, errors.New("controller frame interrupted or exceeded limits")
	}
	return data, nil
}

type response struct {
	Version int             `json:"version"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid controller frame")
	}
	return nil
}

func writeFrame(out io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > maxFrame {
		return errors.New("controller frame exceeds protocol limits")
	}
	data = append(data, '\n')
	for len(data) != 0 {
		n, err := out.Write(data)
		if err != nil || n <= 0 || n > len(data) {
			return errors.New("controller connection interrupted")
		}
		data = data[n:]
	}
	return nil
}

// Serve handles bounded, sequential requests over an already private channel.
// It opens no listener or arbitrary shell/path API. Execution methods require
// an enabled adapter and are never inferred from a request's method name.
// If in is a Closer, cancellation also interrupts an idle read.
func Serve(ctx context.Context, in io.Reader, out io.Writer, reader Reader) error {
	if closer, ok := in.(io.Closer); ok {
		finished := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = closer.Close(); close(finished) })
		defer func() {
			if !stop() {
				<-finished
			}
		}()
	}
	input := bufio.NewReaderSize(in, maxFrame+1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := readFrame(input)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if decode(data, &req) != nil || req.ID == 0 {
			return errors.New("invalid controller request")
		}
		reply := response{Version: ProtocolVersion, ID: req.ID}
		var result any
		err = nil
		methods := supportedMethods(reader)
		operations, _ := reader.(Operator)
		if req.Version != ProtocolVersion {
			reply.Error = "unsupported_protocol"
		} else if req.Method != "hello" && !slices.Contains(methods, req.Method) {
			reply.Error = "unsupported_method"
		} else if req.Method != "apply" && req.Method != "resume" && req.Method != "cancel-preparation" && req.Method != "configure" && req.Method != "initialize" && len(req.Params) != 0 {
			reply.Error = "invalid_params"
		} else {
			switch req.Method {
			case "access":
				if access, ok := reader.(AccessReader); ok {
					result, err = access.Access(ctx)
				} else {
					reply.Error = "unsupported_method"
				}
			case "initialize":
				var params initializationParams
				initializer, ok := reader.(Initializer)
				if !ok {
					reply.Error = "unsupported_method"
					break
				}
				if decode(req.Params, &params) != nil || len(params.Input) == 0 || len(params.Input) > 32<<10 {
					reply.Error = "invalid_params"
					break
				}
				result, err = initializer.Initialize(ctx, params.Input)
			case "configure":
				var params configureParams
				editor, ok := reader.(Configurator)
				if !ok {
					reply.Error = "unsupported_method"
					break
				}
				if decode(req.Params, &params) != nil || len(params.Changes) == 0 || len(params.Changes) > 16 {
					reply.Error = "invalid_params"
					break
				}
				result, err = editor.Configure(ctx, params.Changes)
			case "hello":
				result = capabilities{ProtocolVersion, methods, !slices.Contains(methods, "apply")}
			case "inspect":
				result, err = reader.Inspect(ctx)
			case "plan":
				result, err = reader.Plan(ctx)
			case "runtime":
				result, err = reader.Runtime(ctx)
			case "preflight":
				if preflight, ok := reader.(Preflighter); ok {
					result, err = preflight.Preflight(ctx)
				} else {
					reply.Error = "unsupported_method"
				}
			case "review":
				if operations == nil {
					reply.Error = "unsupported_method"
					break
				}
				result, err = operations.Review(ctx)
			case "operation":
				if operations == nil {
					reply.Error = "unsupported_method"
					break
				}
				result, err = operations.Operation(ctx)
			case "apply":
				var params applyParams
				if decode(req.Params, &params) != nil || !params.Confirmed || !validPublicID(params.ReviewID) {
					reply.Error = "invalid_params"
					break
				}
				if operations == nil {
					reply.Error = "unsupported_method"
					break
				}
				result, err = operations.ApplyReview(ctx, params.ReviewID, params.Confirmed)
			case "resume", "cancel-preparation":
				var params operationParams
				if decode(req.Params, &params) != nil || !validPublicID(params.OperationID) {
					reply.Error = "invalid_params"
					break
				}
				if operations == nil {
					reply.Error = "unsupported_method"
					break
				}
				if req.Method == "resume" {
					result, err = operations.ResumeOperation(ctx, params.OperationID)
				} else {
					result, err = operations.CancelPreparation(ctx, params.OperationID)
				}
			default:
				reply.Error = "unsupported_method"
			}
		}
		if err != nil {
			reply.Error = "operation_failed"
			if req.Method == "initialize" {
				reply.Error = "initialization_failed"
			}
			if req.Method == "configure" {
				reply.Error = "configuration_failed"
			}
			if req.Method == "inspect" || req.Method == "plan" || req.Method == "runtime" || req.Method == "preflight" {
				reply.Error = "inspection_failed"
			}
			switch {
			case errors.Is(err, deployment.ErrStaleReview):
				reply.Error = "stale_review"
			case errors.Is(err, deployment.ErrExpiredReview):
				reply.Error = "expired_review"
			case errors.Is(err, deployment.ErrConsumedReview):
				reply.Error = "consumed_review"
			case errors.Is(err, deployment.ErrExecutionUnavailable):
				reply.Error = "execution_unavailable"
			}
		}
		if reply.Error == "" {
			reply.Result, err = json.Marshal(result)
			if err != nil {
				return errors.New("cannot encode controller result")
			}
		}
		if err := writeFrame(out, reply); err != nil {
			return err
		}
	}
}
