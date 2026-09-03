package capture

const (
	// MaxRetainedWireBodyBytes is the maximum number of wire-format body bytes
	// retained for one captured message. Traffic beyond it is forwarded, but the
	// capture is marked truncated and cannot be used as a complete replay input.
	MaxRetainedWireBodyBytes = 4 << 20 // 4 MiB

	// MaxRetainedLogicalBodyBytes is the maximum decoded body retained for one
	// captured message. It also bounds decompression output.
	MaxRetainedLogicalBodyBytes = 4 << 20 // 4 MiB

	// MaxRetainedHeaderBytes limits all header names and values retained for one
	// message. Header-only traffic must not bypass the capture budget.
	MaxRetainedHeaderBytes = 64 << 10 // 64 KiB

	// MaxRetainedMetaBytes limits protocol-specific metadata retained for one
	// message.
	MaxRetainedMetaBytes = 64 << 10 // 64 KiB

	// MaxRetainedMessageBytes limits a complete serialized wire message,
	// including retained headers and wire body.
	MaxRetainedMessageBytes = MaxRetainedHeaderBytes + MaxRetainedWireBodyBytes

	// MaxRetainedMessageBudgetBytes is the aggregate budget for every retained
	// field of one message, including decoded and wire representations.
	MaxRetainedMessageBudgetBytes = MaxRetainedLogicalBodyBytes + MaxRetainedMessageBytes + MaxRetainedMetaBytes

	// MaxRetainedFlowBytes caps the aggregate footprint of a flow. It applies
	// across request/response and every streaming message.
	MaxRetainedFlowBytes = 16 << 20 // 16 MiB

	// MaxRetainedStoreBytes caps the aggregate footprint of all snapshots held
	// by a Store. Older flows are evicted until the budget is satisfied.
	MaxRetainedStoreBytes = 64 << 20 // 64 MiB

	// MaxFrameBytes is the largest WebSocket or gRPC payload the protocol layer
	// will parse, retain, or make available for tampering.
	MaxFrameBytes = 1 << 20 // 1 MiB

	// MaxMessagesPerFlow is the maximum number of streaming messages retained on
	// a Flow. Further messages still transit but are not retained.
	MaxMessagesPerFlow = 1024

	// MaxFlowsInStore is the number of most-recent flows kept by a Store.
	// Older flows are evicted when this budget is reached.
	MaxFlowsInStore = 2048

	// MaxSessionInputBytes is a hard byte limit for an untrusted serialized
	// session. It includes JSON and base64 expansion as well as retained data.
	MaxSessionInputBytes = 96 << 20 // 96 MiB
)
