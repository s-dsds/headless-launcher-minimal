package ipc

import "encoding/json"

// LaunchMsg matches the TypeScript LaunchMsg interface.
type LaunchMsg struct {
	ScriptPath     StringOrStrings `json:"scriptPath"`
	Token          string          `json:"token"`
	ID             string          `json:"id"`
	HeadlessScript string          `json:"HeadlessScript,omitempty"`
}

// RunScriptMsg matches the TypeScript RunScriptMsg interface.
type RunScriptMsg struct {
	ScriptPaths []string `json:"scriptPaths"`
	ID          string   `json:"id"`
}

// StopMsg matches the TypeScript StopMsg interface.
type StopMsg struct {
	ID string `json:"id"`
}

// FollowMsg matches the TypeScript FollowMsg interface.
type FollowMsg struct {
	ID string `json:"id"`
}

// MetricsMsg matches the TypeScript MetricsMsg interface.
type MetricsMsg struct {
	ID string `json:"id"`
}

// MetricsResultMsg matches the TypeScript MetricsResultMsg interface.
type MetricsResultMsg struct {
	Timestamp       float64 `json:"Timestamp"`
	ID              string  `json:"Id"`
	ScriptDuration  float64 `json:"ScriptDuration"`
	TaskDuration    float64 `json:"TaskDuration"`
	JSHeapUsedSize  float64 `json:"JSHeapUsedSize"`
	JSHeapTotalSize float64 `json:"JSHeapTotalSize"`
}

// StringOrStrings handles the TypeScript `string | string[]` union for scriptPath.
type StringOrStrings []string

func (s *StringOrStrings) UnmarshalJSON(data []byte) error {
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	*s = []string{str}
	return nil
}

func (s StringOrStrings) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(s))
}
