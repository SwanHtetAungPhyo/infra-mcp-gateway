package types

import "bytes"

type (
	DataPayload struct {
		ToolName string
		Payload  bytes.Buffer
	}

	NmapResult struct {
		Run   NmapRun
		Err   error
		RunId string
	}
)
