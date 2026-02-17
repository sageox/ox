package testutil

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/pkg/faultdaemon"
)

// OxFaultConfig extends generic fault config with ox-specific responses.
type OxFaultConfig struct {
	// Embedded generic config
	faultdaemon.Config

	// StatusResponse is returned for status requests.
	StatusResponse *daemon.StatusData

	// DoctorResponse is returned for doctor requests.
	DoctorResponse *daemon.DoctorResponse

	// CheckoutResponse is returned for checkout requests.
	CheckoutResponse *daemon.CheckoutResult

	// SyncError is the error to return for sync requests (nil = success).
	SyncError error

	// CustomResponses maps message types to custom response data.
	// Value will be marshaled to JSON and wrapped in daemon.Response.
	CustomResponses map[string]any
}

// OxFaultDaemon wraps the generic FaultDaemon with ox protocol knowledge.
// It generates ox-specific responses (daemon.Response) for incoming messages.
type OxFaultDaemon struct {
	*faultdaemon.FaultDaemon

	t        *testing.T
	oxConfig OxFaultConfig
}

// NewOxFaultDaemon creates an ox-specific fault daemon for testing.
// It understands ox IPC protocol and generates appropriate responses.
func NewOxFaultDaemon(t *testing.T, config OxFaultConfig) *OxFaultDaemon {
	t.Helper()

	// create response handler that generates ox protocol responses
	config.ResponseHandler = func(request []byte) []byte {
		return generateOxResponse(request, config)
	}

	// wrap FaultMatcher if FaultOnMessageType is set
	if config.FaultMatcher == nil && faultOnMsgType(config) != "" {
		msgType := faultOnMsgType(config)
		config.FaultMatcher = func(request []byte) bool {
			var msg daemon.Message
			if err := json.Unmarshal(request, &msg); err != nil {
				return false
			}
			return msg.Type == msgType
		}
	}

	d := faultdaemon.New(
		daemon.SocketPath(),
		config.Config,
	)

	return &OxFaultDaemon{
		FaultDaemon: d,
		t:           t,
		oxConfig:    config,
	}
}

// faultOnMsgType extracts FaultOnMessageType from config if set.
// We store it in CustomResponses for backward compat with old tests.
func faultOnMsgType(config OxFaultConfig) string {
	if config.CustomResponses == nil {
		return ""
	}
	if v, ok := config.CustomResponses["_fault_on_msg_type"].(string); ok {
		return v
	}
	return ""
}

// Start starts the ox fault daemon.
func (d *OxFaultDaemon) Start() {
	d.t.Helper()
	if err := d.FaultDaemon.Start(); err != nil {
		d.t.Fatalf("failed to start ox fault daemon: %v", err)
	}
	// wait for socket to be ready
	time.Sleep(50 * time.Millisecond)
}

// SetOxConfig updates the ox-specific configuration.
func (d *OxFaultDaemon) SetOxConfig(config OxFaultConfig) {
	config.ResponseHandler = func(request []byte) []byte {
		return generateOxResponse(request, config)
	}
	d.SetConfig(config.Config)
	d.oxConfig = config
}

// generateOxResponse generates an ox protocol response for a request.
func generateOxResponse(request []byte, config OxFaultConfig) []byte {
	var msg daemon.Message
	if err := json.Unmarshal(request, &msg); err != nil {
		return marshalOxResponse(daemon.Response{Success: false, Error: "invalid message"})
	}

	var resp daemon.Response

	switch msg.Type {
	case daemon.MsgTypePing:
		resp = daemon.Response{Success: true, Data: json.RawMessage(`"pong"`)}

	case daemon.MsgTypeStatus:
		status := config.StatusResponse
		if status == nil {
			status = &daemon.StatusData{
				Running: true,
				Pid:     os.Getpid(),
				Version: "ox-fault-daemon-test",
			}
		}
		data, _ := json.Marshal(status)
		resp = daemon.Response{Success: true, Data: data}

	case daemon.MsgTypeSessions:
		sessionsResp := &daemon.SessionsResponse{Sessions: []daemon.AgentSession{}}
		data, _ := json.Marshal(sessionsResp)
		resp = daemon.Response{Success: true, Data: data}

	case daemon.MsgTypeInstances:
		instancesResp := &daemon.InstancesResponse{Instances: []daemon.InstanceInfo{}}
		data, _ := json.Marshal(instancesResp)
		resp = daemon.Response{Success: true, Data: data}

	case daemon.MsgTypeSyncHistory:
		resp = daemon.Response{Success: true, Data: json.RawMessage(`[]`)}

	case daemon.MsgTypeDoctor:
		doctorResp := config.DoctorResponse
		if doctorResp == nil {
			doctorResp = &daemon.DoctorResponse{
				AntiEntropyTriggered: false,
				ClonesTriggered:      0,
				Errors:               []string{},
			}
		}
		data, _ := json.Marshal(doctorResp)
		resp = daemon.Response{Success: true, Data: data}

	case daemon.MsgTypeSync, daemon.MsgTypeTeamSync:
		if config.SyncError != nil {
			resp = daemon.Response{Success: false, Error: config.SyncError.Error()}
		} else {
			resp = daemon.Response{Success: true}
		}

	case daemon.MsgTypeStop:
		resp = daemon.Response{Success: true}

	case daemon.MsgTypeGetErrors:
		resp = daemon.Response{Success: true, Data: json.RawMessage(`[]`)}

	case daemon.MsgTypeMarkErrors:
		resp = daemon.Response{Success: true}

	case daemon.MsgTypeCheckout:
		result := config.CheckoutResponse
		if result == nil {
			result = &daemon.CheckoutResult{
				Path:          "/tmp/test-checkout",
				AlreadyExists: false,
				Cloned:        true,
			}
		}
		data, _ := json.Marshal(result)
		resp = daemon.Response{Success: true, Data: data}

	case daemon.MsgTypeHeartbeat, daemon.MsgTypeTelemetry, daemon.MsgTypeFriction:
		// fire-and-forget messages - still send response for consistency
		return nil

	default:
		// check for custom response
		if config.CustomResponses != nil {
			if customResp, ok := config.CustomResponses[msg.Type]; ok {
				data, _ := json.Marshal(customResp)
				resp = daemon.Response{Success: true, Data: data}
				return marshalOxResponse(resp)
			}
		}
		resp = daemon.Response{Success: true}
	}

	return marshalOxResponse(resp)
}

func marshalOxResponse(resp daemon.Response) []byte {
	data, _ := json.Marshal(resp)
	return append(data, '\n')
}

// --- Convenience constructors that mirror the old FaultDaemon API ---

// NewOxHungDaemon creates a daemon that accepts connections but never responds.
func NewOxHungDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxFaultDaemon(t, OxFaultConfig{
		Config: faultdaemon.Config{Fault: faultdaemon.FaultHangBeforeResponse},
	})
}

// NewOxSlowDaemon creates a daemon that responds after a delay.
func NewOxSlowDaemon(t *testing.T, delay time.Duration) *OxFaultDaemon {
	return NewOxFaultDaemon(t, OxFaultConfig{
		Config: faultdaemon.Config{
			Fault:             faultdaemon.FaultSlowResponse,
			SlowResponseDelay: delay,
		},
	})
}

// NewOxCrashingDaemon creates a daemon that closes connections immediately.
func NewOxCrashingDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxFaultDaemon(t, OxFaultConfig{
		Config: faultdaemon.Config{Fault: faultdaemon.FaultCloseImmediately},
	})
}

// NewOxCorruptDaemon creates a daemon that sends garbage responses.
func NewOxCorruptDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxFaultDaemon(t, OxFaultConfig{
		Config: faultdaemon.Config{Fault: faultdaemon.FaultCorruptResponse},
	})
}

// NewOxFlakyDaemon creates a daemon that drops every Nth connection.
func NewOxFlakyDaemon(t *testing.T, dropEveryN int) *OxFaultDaemon {
	return NewOxFaultDaemon(t, OxFaultConfig{
		Config: faultdaemon.Config{
			Fault:      faultdaemon.FaultDropConnection,
			DropEveryN: dropEveryN,
		},
	})
}

// NewOxHealthyFaultDaemon creates a fault daemon with no faults (for baseline testing).
func NewOxHealthyFaultDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxFaultDaemon(t, OxFaultConfig{
		Config: faultdaemon.Config{Fault: faultdaemon.FaultNone},
	})
}

// =============================================================================
// BACKWARD COMPATIBILITY API
// These maintain compatibility with tests written against the old API.
// =============================================================================

// Fault type alias for backward compatibility
type Fault = faultdaemon.Fault

// FaultConfig is the backward-compatible config struct for tests.
// It mirrors the old API where fields were at the top level.
type FaultConfig struct {
	// Fault is the failure mode to inject.
	Fault Fault

	// SlowResponseDelay is the delay for slow response faults.
	SlowResponseDelay time.Duration

	// DropEveryN drops every Nth connection for drop connection fault.
	DropEveryN int

	// FaultOnMessageType restricts the fault to a specific message type.
	// Other message types respond normally.
	FaultOnMessageType string

	// StatusResponse overrides the daemon's status response.
	StatusResponse *daemon.StatusData

	// DoctorResponse overrides the daemon's doctor response.
	DoctorResponse *daemon.DoctorResponse

	// CheckoutResponse overrides the daemon's checkout response.
	CheckoutResponse *daemon.CheckoutResult

	// SyncError is the error to return for sync requests.
	SyncError error
}

// Fault constant aliases
const (
	FaultNone                   = faultdaemon.FaultNone
	FaultHangOnAccept           = faultdaemon.FaultHangOnAccept
	FaultHangBeforeResponse     = faultdaemon.FaultHangBeforeResponse
	FaultSlowResponse           = faultdaemon.FaultSlowResponse
	FaultCorruptResponse        = faultdaemon.FaultCorruptResponse
	FaultPartialResponse        = faultdaemon.FaultPartialResponse
	FaultCloseImmediately       = faultdaemon.FaultCloseImmediately
	FaultCloseAfterRead         = faultdaemon.FaultCloseAfterRead
	FaultDropConnection         = faultdaemon.FaultDropConnection
	FaultPanicInHandler         = faultdaemon.FaultPanicInHandler
	FaultDeadlock               = faultdaemon.FaultDeadlock
	FaultMultipleResponses      = faultdaemon.FaultMultipleResponses
	FaultResponseWithoutNewline = faultdaemon.FaultResponseWithoutNewline
	FaultChunkedResponse        = faultdaemon.FaultChunkedResponse
	FaultSlowAccept             = faultdaemon.FaultSlowAccept
	FaultRefuseAfterAccept      = faultdaemon.FaultRefuseAfterAccept
	FaultVerySlowResponse       = faultdaemon.FaultVerySlowResponse
	FaultResponseTooLarge       = faultdaemon.FaultResponseTooLarge
	FaultInvalidJSON            = faultdaemon.FaultInvalidJSON
	FaultEmbeddedNewlines       = faultdaemon.FaultEmbeddedNewlines
	FaultWriteHalfThenHang      = faultdaemon.FaultWriteHalfThenHang
)

// toOxFaultConfig converts the backward-compatible FaultConfig to OxFaultConfig.
func toOxFaultConfig(fc FaultConfig) OxFaultConfig {
	oxc := OxFaultConfig{
		Config: faultdaemon.Config{
			Fault:             fc.Fault,
			SlowResponseDelay: fc.SlowResponseDelay,
			DropEveryN:        fc.DropEveryN,
		},
		StatusResponse:   fc.StatusResponse,
		DoctorResponse:   fc.DoctorResponse,
		CheckoutResponse: fc.CheckoutResponse,
		SyncError:        fc.SyncError,
	}

	// handle FaultOnMessageType via CustomResponses hack
	if fc.FaultOnMessageType != "" {
		oxc.CustomResponses = map[string]any{
			"_fault_on_msg_type": fc.FaultOnMessageType,
		}
	}

	return oxc
}

// NewFaultDaemon creates an ox fault daemon with the given config.
func NewFaultDaemon(t *testing.T, config FaultConfig) *OxFaultDaemon {
	return NewOxFaultDaemon(t, toOxFaultConfig(config))
}

// NewHealthyFaultDaemon creates a fault daemon with no faults.
func NewHealthyFaultDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxHealthyFaultDaemon(t)
}

// NewCrashingDaemon creates a daemon that closes connections immediately.
func NewCrashingDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxCrashingDaemon(t)
}

// NewCorruptDaemon creates a daemon that sends garbage responses.
func NewCorruptDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxCorruptDaemon(t)
}

// NewHungDaemon creates a daemon that accepts connections but never responds.
func NewHungDaemon(t *testing.T) *OxFaultDaemon {
	return NewOxHungDaemon(t)
}

// NewSlowDaemon creates a daemon that responds after a delay.
func NewSlowDaemon(t *testing.T, delay time.Duration) *OxFaultDaemon {
	return NewOxSlowDaemon(t, delay)
}

// NewFlakyDaemon creates a daemon that drops every Nth connection.
func NewFlakyDaemon(t *testing.T, dropEveryN int) *OxFaultDaemon {
	return NewOxFlakyDaemon(t, dropEveryN)
}
